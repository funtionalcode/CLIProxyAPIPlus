package management

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func validRenameAuthFileName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && len(name) <= 200 &&
		!strings.HasPrefix(name, ".") && !strings.ContainsAny(name, "/\\:\x00\r\n") &&
		filepath.Base(name) == name && strings.HasSuffix(strings.ToLower(name), ".json")
}

// RenameAuthFileV8 replaces a verified Gemini Web record under a new filename.
// The destination is published without overwrite; credential mutations are held
// until the old file is removed so a refresh cannot recreate it during the move.
func (h *Handler) RenameAuthFileV8(c *gin.Context) {
	if h.cfg == nil || h.authManager == nil {
		c.JSON(503, gin.H{"error": "auth manager unavailable"})
		return
	}
	var req struct {
		Name    string `json:"name"`
		NewName string `json:"new_name"`
		Content string `json:"content"`
	}
	if c.ShouldBindJSON(&req) != nil || !validRenameAuthFileName(req.Name) || !validRenameAuthFileName(req.NewName) || req.Name == req.NewName {
		c.JSON(400, gin.H{"error": "valid distinct .json filenames are required"})
		return
	}
	guard, err := authFileWriteGuardFromRequest(c)
	if err != nil || guard == nil {
		c.JSON(400, gin.H{"error": "source identity and content hash are required"})
		return
	}
	h.authStatusMu.Lock()
	defer h.authStatusMu.Unlock()
	source := filepath.Join(h.cfg.AuthDir, req.Name)
	target := filepath.Join(h.cfg.AuthDir, req.NewName)
	info, err := os.Lstat(source)
	if err != nil {
		c.JSON(404, gin.H{"error": "source auth file not found"})
		return
	}
	if !info.Mode().IsRegular() {
		c.JSON(400, gin.H{"error": "source must be a regular auth file"})
		return
	}
	if err = guard.validate(req.Name, source); err != nil {
		c.JSON(409, gin.H{"error": "source changed; reload before renaming"})
		return
	}
	metadata, err := decodeAuthFileMetadata([]byte(req.Content))
	if err != nil || metadata["type"] != "gemini-web" {
		c.JSON(400, gin.H{"error": "rename supports Gemini Web account files only"})
		return
	}
	if session, ok := metadata["secure_1psid"].(string); !ok || strings.TrimSpace(session) == "" {
		c.JSON(400, gin.H{"error": "Gemini Web session is required"})
		return
	}
	old := h.findAuthForDelete(req.Name)
	if old == nil || old.Provider != "gemini-web" || coreauth.IsPluginVirtualAuth(old) || old.Attributes[coreauth.AttributeSourceBackend] != coreauth.AuthSourceFile {
		c.JSON(409, gin.H{"error": "source must be a single file-backed Gemini Web account"})
		return
	}
	if _, exists := h.authManager.GetByID(h.authIDForPath(target)); exists {
		c.JSON(409, gin.H{"error": "destination account already exists"})
		return
	}
	next, err := h.buildAuthFromFileMetadata(target, []byte(req.Content), metadata)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid account metadata"})
		return
	}
	ctx := c.Request.Context()
	err = h.authManager.RemoveWithAction(ctx, old.ID, func(current *coreauth.Auth) error {
		if current.Provider != "gemini-web" {
			return errAuthFileWriteConflict
		}
		if err := guard.validate(req.Name, source); err != nil {
			return err
		}
		file, err := os.CreateTemp(h.cfg.AuthDir, ".gemini-rename-*")
		if err != nil {
			return err
		}
		temp := file.Name()
		defer func() {
			if errRemove := os.Remove(temp); errRemove != nil && !os.IsNotExist(errRemove) {
				log.WithError(errRemove).Warn("remove rename temporary file")
			}
		}()
		_, errWrite := file.WriteString(req.Content)
		errClose := file.Close()
		if errWrite != nil {
			return errWrite
		}
		if errClose != nil {
			return errClose
		}
		if err = os.Link(temp, target); err != nil {
			return err
		}
		if err = os.Remove(source); err != nil {
			if errRollback := os.Remove(target); errRollback != nil {
				log.WithError(errRollback).Error("rollback credential rename")
			}
			return err
		}
		return nil
	})
	if err != nil {
		if os.IsExist(err) {
			c.JSON(409, gin.H{"error": "destination filename already exists"})
			return
		}
		if errors.Is(err, errAuthFileWriteConflict) {
			c.JSON(409, gin.H{"error": "source changed; reload before renaming"})
			return
		}
		log.WithError(err).Error("rename Gemini Web account")
		c.JSON(500, gin.H{"error": "could not rename account file"})
		return
	}
	registry.GetGlobalRegistry().UnregisterClient(old.ID)
	// Persistence is already complete; the file watcher can retry runtime registration.
	_, err = h.authManager.Register(coreauth.WithSkipPersist(ctx), next)
	if err == nil && h.postAuthPersistHook != nil {
		err = h.postAuthPersistHook(ctx, next)
	}
	if err != nil {
		log.WithError(err).Error("synchronize renamed Gemini Web account")
		c.JSON(http.StatusOK, gin.H{"status": "ok", "name": req.NewName, "warning": "file renamed; runtime synchronization pending"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "name": req.NewName})
}
