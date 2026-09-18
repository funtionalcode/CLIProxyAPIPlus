package turnstate

import (
	"fmt"
	"sort"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ProbeAccount contains only management-facing account details and never exposes tokens.
type ProbeAccount struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func probeAccountToken(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	token, _ := a.Metadata["access_token"].(string)
	return strings.TrimSpace(token)
}

func probeAccountDisabled(a *coreauth.Auth) bool {
	return a == nil || a.Disabled || a.Status == coreauth.StatusDisabled
}

func probeAccountReason(a *coreauth.Auth) string {
	if a == nil || !strings.EqualFold(a.Provider, "codex") {
		return "所选 Codex 账户不存在"
	}
	if probeAccountDisabled(a) {
		return "已禁用"
	}
	if probeAccountToken(a) == "" {
		return "未登录"
	}
	if a.Status == coreauth.StatusError || a.Unavailable || !a.NextRetryAfter.IsZero() {
		return "冷却中"
	}
	if !a.HasValidAccessToken(time.Now()) {
		return "登录已过期"
	}
	return ""
}

func probeAccountError(a *coreauth.Auth) error {
	if a == nil || !strings.EqualFold(a.Provider, "codex") {
		return fmt.Errorf("所选 Codex 账户不存在")
	}
	if probeAccountDisabled(a) {
		return fmt.Errorf("所选账户已禁用")
	}
	if probeAccountToken(a) == "" {
		return fmt.Errorf("所选账户未登录，请先完成 OAuth 授权")
	}
	return nil
}

// ListProbeAccounts returns selectable Codex login accounts and any availability reason.
func ListProbeAccounts(manager *coreauth.Manager) []ProbeAccount {
	accounts := make([]ProbeAccount, 0)
	if manager == nil {
		return accounts
	}
	for _, a := range manager.List() {
		if a == nil || !strings.EqualFold(a.Provider, "codex") || a.Attributes["api_key"] != "" {
			continue
		}
		label := strings.TrimSpace(a.Label)
		if label == "" {
			label, _ = a.Metadata["email"].(string)
		}
		if label == "" {
			label = a.ID
		}
		entry := ProbeAccount{ID: a.ID, Label: label, Available: probeAccountError(a) == nil, Reason: probeAccountReason(a)}
		accounts = append(accounts, entry)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return accounts
}

// ResolveProbeAccount resolves the latest token by exact ID without falling back to another account.
func ResolveProbeAccount(manager *coreauth.Manager, requestedID string) (apiKey, authID, accountID string, err error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == "" {
		return "", "", "", fmt.Errorf("请先选择用于探针的已登录账户")
	}
	if manager == nil {
		return "", "", "", fmt.Errorf("账户管理服务不可用")
	}
	a, _ := manager.GetByID(requestedID)
	if err = probeAccountError(a); err != nil {
		return "", "", "", err
	}
	apiKey = probeAccountToken(a)
	accountID, _ = a.Metadata["account_id"].(string)
	return apiKey, a.ID, accountID, nil
}
