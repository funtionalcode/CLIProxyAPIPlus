package management

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDecodeLogCursorRejectsUnsafeFiles(t *testing.T) {
	unsafeNames := []string{
		"",
		".",
		"..",
		"../secret",
		"nested/main.log",
		`nested\main.log`,
		"error.log",
	}

	for _, name := range unsafeNames {
		t.Run(name, func(t *testing.T) {
			raw := mustEncodeRawCursor(t, logCursor{
				Version:     logCursorVersion,
				File:        name,
				Fingerprint: "fingerprint",
			})
			if _, err := decodeLogCursor(raw); err == nil {
				t.Fatalf("decodeLogCursor(%q) succeeded, want error", name)
			}
		})
	}

	for _, name := range []string{defaultLogFileName, defaultLogFileName + ".1", "main-2026-06-15T10-00-00.log"} {
		t.Run("allowed_"+name, func(t *testing.T) {
			raw := mustEncodeRawCursor(t, logCursor{
				Version:     logCursorVersion,
				File:        name,
				Fingerprint: "fingerprint",
			})
			if _, err := decodeLogCursor(raw); err != nil {
				t.Fatalf("decodeLogCursor(%q) error = %v", name, err)
			}
		})
	}
}

func TestLogCursorRoundTripOmitsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultLogFileName)
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	boundary, errBoundary := completeLogBoundary(path)
	if errBoundary != nil {
		t.Fatalf("completeLogBoundary() error = %v", errBoundary)
	}
	raw, errCursor := newLogCursor(path, boundary, 123)
	if errCursor != nil {
		t.Fatalf("newLogCursor() error = %v", errCursor)
	}
	decoded, errDecode := decodeLogCursor(raw)
	if errDecode != nil {
		t.Fatalf("decodeLogCursor() error = %v", errDecode)
	}
	if decoded.File != defaultLogFileName {
		t.Fatalf("cursor file = %q, want %q", decoded.File, defaultLogFileName)
	}
	if decoded.Offset != boundary {
		t.Fatalf("cursor offset = %d, want %d", decoded.Offset, boundary)
	}
	if decoded.LatestTimestamp != 123 {
		t.Fatalf("cursor latest timestamp = %d, want 123", decoded.LatestTimestamp)
	}
	if strings.Contains(raw, dir) {
		t.Fatalf("encoded cursor contains log directory %q: %q", dir, raw)
	}
}

func TestReadCompleteLogLinesSkipsTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultLogFileName)
	initial := "first\nsecond\r\npartial"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	read, errRead := readCompleteLogLines(path, 0, -1, 0)
	if errRead != nil {
		t.Fatalf("readCompleteLogLines() error = %v", errRead)
	}
	wantLines := []string{"first", "second"}
	if !reflect.DeepEqual(read.lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", read.lines, wantLines)
	}
	wantOffset := int64(len("first\nsecond\r\n"))
	if read.endOffset != wantOffset {
		t.Fatalf("endOffset = %d, want %d", read.endOffset, wantOffset)
	}

	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if errOpen != nil {
		t.Fatalf("open log file: %v", errOpen)
	}
	if _, errWrite := file.WriteString("\n"); errWrite != nil {
		_ = file.Close()
		t.Fatalf("append newline: %v", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("close log file: %v", errClose)
	}

	next, errNext := readCompleteLogLines(path, read.endOffset, -1, 0)
	if errNext != nil {
		t.Fatalf("readCompleteLogLines() after append error = %v", errNext)
	}
	if !reflect.DeepEqual(next.lines, []string{"partial"}) {
		t.Fatalf("next lines = %#v, want partial", next.lines)
	}
	if next.endOffset != int64(len(initial)+1) {
		t.Fatalf("next endOffset = %d, want %d", next.endOffset, len(initial)+1)
	}
}

func TestGetLogsTailLimitReturnsRecentLinesWithCursor(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
		"[2026-06-15 10:00:03] fourth",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=2")
	wantLines := []string{lines[2], lines[3]}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != len(wantLines) {
		t.Fatalf("line-count = %d, want returned line count %d", resp.LineCount, len(wantLines))
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
	wantLatest := time.Date(2026, 6, 15, 10, 0, 3, 0, time.Local).Unix()
	if resp.LatestTimestamp != wantLatest {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, wantLatest)
	}
}

func TestGetLogsTailLimitDoesNotScanOlderFilesForLineCount(t *testing.T) {
	dir := t.TempDir()
	rotatedPath := filepath.Join(dir, defaultLogFileName+".1")
	if err := os.WriteFile(rotatedPath, []byte(strings.Repeat("x", logScannerMaxBuffer+1)+"\n"), 0o644); err != nil {
		t.Fatalf("write rotated log: %v", err)
	}
	writeMainLog(t, dir, "[2026-06-15 10:00:00] current\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	wantLines := []string{"[2026-06-15 10:00:00] current"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != len(wantLines) {
		t.Fatalf("line-count = %d, want returned line count %d", resp.LineCount, len(wantLines))
	}
}

func TestGetLogsNoLimitKeepsFullScanBehavior(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "complete\npartial")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs")
	wantLines := []string{"complete", "partial"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 2 {
		t.Fatalf("line-count = %d, want full scan count 2", resp.LineCount)
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(resp.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode next-cursor: %v", errCursor)
	}
	if cursor.Offset != int64(len("complete\n")) {
		t.Fatalf("cursor offset = %d, want complete-line boundary", cursor.Offset)
	}
}

func TestGetLogsAfterKeepsTimestampScanAndReturnsCursor(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")

	cutoff := time.Date(2026, 6, 15, 10, 0, 0, 0, time.Local).Unix()
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?after="+strconv.FormatInt(cutoff, 10))
	wantLines := []string{lines[1], lines[2]}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 3 {
		t.Fatalf("line-count = %d, want full scan count 3", resp.LineCount)
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
}

func TestGetLogsCursorReturnsOnlyNewCompleteLines(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=2")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}

	appendMainLog(t, dir, "[2026-06-15 10:00:03] fourth\n")
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	wantLines := []string{"[2026-06-15 10:00:03] fourth"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 1 {
		t.Fatalf("line-count = %d, want 1", resp.LineCount)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}
	wantLatest := time.Date(2026, 6, 15, 10, 0, 3, 0, time.Local).Unix()
	if resp.LatestTimestamp != wantLatest {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, wantLatest)
	}
}

func TestGetLogsCursorRejectsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "[2026-06-15 10:00:00] first\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}

	appendMainLog(t, dir, strings.Repeat("x", logScannerMaxBuffer+1)+"\n")
	status, body := performGetLogsRaw(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if !strings.Contains(body, "log line exceeds") {
		t.Fatalf("body = %s, want oversized line error", body)
	}
}

func TestGetLogsCursorNoNewLinesKeepsCursorStable(t *testing.T) {
	dir := t.TempDir()
	line := "[2026-06-15 10:00:00] first"
	writeMainLog(t, dir, line+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	if len(resp.Lines) != 0 {
		t.Fatalf("lines = %#v, want empty", resp.Lines)
	}
	if resp.LineCount != 0 {
		t.Fatalf("line-count = %d, want 0", resp.LineCount)
	}
	if resp.NextCursor != initial.NextCursor {
		t.Fatalf("next-cursor changed with no complete lines")
	}
	if resp.LatestTimestamp != initial.LatestTimestamp {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, initial.LatestTimestamp)
	}
}

func TestGetLogsCursorDoesNotAdvancePastTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	line := "[2026-06-15 10:00:00] first"
	writeMainLog(t, dir, line+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	appendMainLog(t, dir, "partial")
	partial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	if len(partial.Lines) != 0 {
		t.Fatalf("partial lines = %#v, want empty", partial.Lines)
	}
	if partial.NextCursor != initial.NextCursor {
		t.Fatalf("cursor advanced past partial line")
	}

	appendMainLog(t, dir, "\n")
	complete := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	if !reflect.DeepEqual(complete.Lines, []string{"partial"}) {
		t.Fatalf("complete lines = %#v, want partial", complete.Lines)
	}
	if complete.LatestTimestamp != initial.LatestTimestamp {
		t.Fatalf("latest-timestamp = %d, want %d", complete.LatestTimestamp, initial.LatestTimestamp)
	}
}

func TestGetLogsCursorResetAfterTruncateTailsLimit(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=3")

	resetLine := "[2026-06-15 10:00:03] reset"
	writeMainLog(t, dir, resetLine+"\n")
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true")
	}
	if !reflect.DeepEqual(resp.Lines, []string{resetLine}) {
		t.Fatalf("lines = %#v, want reset tail", resp.Lines)
	}
	if resp.LineCount != 1 {
		t.Fatalf("line-count = %d, want 1", resp.LineCount)
	}
}

func TestGetLogsCursorReadsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	line1 := "[2026-06-15 10:00:00] first"
	line2 := "[2026-06-15 10:00:01] second"
	line3 := "[2026-06-15 10:00:02] third"
	writeMainLog(t, dir, line1+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	appendMainLog(t, dir, line2+"\n")
	if err := os.Rename(filepath.Join(dir, defaultLogFileName), filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, line3+"\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	wantLines := []string{line2, line3}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}
}

func TestGetLogsCursorReadsRotatedFileWhenNewMainIsSmaller(t *testing.T) {
	dir := t.TempDir()
	line1 := "[2026-06-15 10:00:00] first line with enough bytes"
	line2 := "[2026-06-15 10:00:01] second"
	line3 := "new"
	writeMainLog(t, dir, line1+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	appendMainLog(t, dir, line2+"\n")
	if err := os.Rename(filepath.Join(dir, defaultLogFileName), filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, line3+"\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(resp.Lines, []string{line2}) {
		t.Fatalf("lines = %#v, want rotated unread line", resp.Lines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}

	next := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(resp.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(next.Lines, []string{line3}) {
		t.Fatalf("next lines = %#v, want new main line", next.Lines)
	}
	if next.CursorReset {
		t.Fatal("next cursor-reset = true, want false")
	}
}

func TestGetLogsZeroOffsetCursorWithPartialLineReadsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "partial")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(initial.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode initial cursor: %v", errCursor)
	}
	if cursor.Offset != 0 || cursor.Size == 0 {
		t.Fatalf("cursor offset/size = %d/%d, want zero offset with partial size", cursor.Offset, cursor.Size)
	}

	appendMainLog(t, dir, " complete\n")
	if err := os.Rename(filepath.Join(dir, defaultLogFileName), filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, "new\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	wantLines := []string{"partial complete", "new"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}
}

func TestGetLogsZeroOffsetCursorWithEmptyFileReadsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(initial.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode initial cursor: %v", errCursor)
	}
	if cursor.Offset != 0 || cursor.Size != 0 {
		t.Fatalf("cursor offset/size = %d/%d, want empty zero offset", cursor.Offset, cursor.Size)
	}

	appendMainLog(t, dir, "first\n")
	mainPath := filepath.Join(dir, defaultLogFileName)
	nextModTime := time.Unix(0, cursorModTimeUnixNano(cursor)+int64(time.Second))
	if err := os.Chtimes(mainPath, nextModTime, nextModTime); err != nil {
		t.Fatalf("update main log mtime: %v", err)
	}
	if err := os.Rename(mainPath, filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, "second\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(resp.Lines, []string{"first"}) {
		t.Fatalf("lines = %#v, want first rotated line", resp.Lines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}

	next := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(resp.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(next.Lines, []string{"second"}) {
		t.Fatalf("next lines = %#v, want second main line", next.Lines)
	}
	if next.CursorReset {
		t.Fatal("next cursor-reset = true, want false")
	}
}

func TestGetLogsZeroOffsetCursorWithEmptyFileReadsAcrossTwoRotations(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(initial.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode initial cursor: %v", errCursor)
	}
	if cursor.Offset != 0 || cursor.Size != 0 {
		t.Fatalf("cursor offset/size = %d/%d, want empty zero offset", cursor.Offset, cursor.Size)
	}

	mainPath := filepath.Join(dir, defaultLogFileName)
	firstRotatedPath := filepath.Join(dir, defaultLogFileName+".1")
	secondRotatedPath := filepath.Join(dir, defaultLogFileName+".2")
	firstModTime := time.Unix(0, cursorModTimeUnixNano(cursor)+int64(time.Second))
	secondModTime := time.Unix(0, cursorModTimeUnixNano(cursor)+2*int64(time.Second))

	appendMainLog(t, dir, "first\n")
	if err := os.Chtimes(mainPath, firstModTime, firstModTime); err != nil {
		t.Fatalf("update first main log mtime: %v", err)
	}
	if err := os.Rename(mainPath, firstRotatedPath); err != nil {
		t.Fatalf("rotate first main log: %v", err)
	}
	writeMainLog(t, dir, "second\n")
	if err := os.Chtimes(mainPath, secondModTime, secondModTime); err != nil {
		t.Fatalf("update second main log mtime: %v", err)
	}
	if err := os.Rename(firstRotatedPath, secondRotatedPath); err != nil {
		t.Fatalf("advance first rotated log: %v", err)
	}
	if err := os.Rename(mainPath, firstRotatedPath); err != nil {
		t.Fatalf("rotate second main log: %v", err)
	}
	writeMainLog(t, dir, "third\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(resp.Lines, []string{"first"}) {
		t.Fatalf("lines = %#v, want oldest rotated line", resp.Lines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}

	next := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(resp.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(next.Lines, []string{"second"}) {
		t.Fatalf("next lines = %#v, want newer rotated line", next.Lines)
	}
	if next.CursorReset {
		t.Fatal("next cursor-reset = true, want false")
	}

	latest := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(next.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(latest.Lines, []string{"third"}) {
		t.Fatalf("latest lines = %#v, want main line", latest.Lines)
	}
	if latest.CursorReset {
		t.Fatal("latest cursor-reset = true, want false")
	}
}

func TestGetLogsZeroOffsetCursorWithEmptyFileResetsWhenRotationModTimeAmbiguous(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, defaultLogFileName)
	fixedModTime := time.Date(2026, 6, 15, 10, 0, 0, 0, time.Local)
	writeMainLog(t, dir, "")
	if err := os.Chtimes(mainPath, fixedModTime, fixedModTime); err != nil {
		t.Fatalf("set initial main mtime: %v", err)
	}
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(initial.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode initial cursor: %v", errCursor)
	}
	if cursor.Offset != 0 || cursor.Size != 0 {
		t.Fatalf("cursor offset/size = %d/%d, want empty zero offset", cursor.Offset, cursor.Size)
	}

	first := "[2026-06-15 10:00:01] first"
	second := "[2026-06-15 10:00:02] second"
	appendMainLog(t, dir, first+"\n")
	if err := os.Chtimes(mainPath, fixedModTime, fixedModTime); err != nil {
		t.Fatalf("set rotated mtime: %v", err)
	}
	if err := os.Rename(mainPath, filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, second+"\n")
	if err := os.Chtimes(mainPath, fixedModTime, fixedModTime); err != nil {
		t.Fatalf("set new main mtime: %v", err)
	}

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=2")
	wantLines := []string{first, second}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true for ambiguous empty cursor rotation")
	}
	if resp.LineCount != len(wantLines) {
		t.Fatalf("line-count = %d, want returned line count %d", resp.LineCount, len(wantLines))
	}
}

func TestGetLogsInvalidCursorResetsToTail(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")

	cases := []string{
		"not-base64",
		mustEncodeRawCursor(t, logCursor{
			Version:     logCursorVersion,
			File:        "../secret",
			Fingerprint: "fingerprint",
		}),
	}
	for _, raw := range cases {
		resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(raw)+"&limit=1")
		if !resp.CursorReset {
			t.Fatalf("cursor-reset = false for cursor %q", raw)
		}
		if !reflect.DeepEqual(resp.Lines, []string{lines[1]}) {
			t.Fatalf("lines = %#v, want latest line", resp.Lines)
		}
		if resp.LineCount != 1 {
			t.Fatalf("line-count = %d, want 1", resp.LineCount)
		}
	}
}

func TestGetLogsMissingRotatedCursorFileResetsToTail(t *testing.T) {
	dir := t.TempDir()
	current := "[2026-06-15 10:00:01] current"
	writeMainLog(t, dir, current+"\n")
	rotatedPath := filepath.Join(dir, defaultLogFileName+".1")
	if err := os.WriteFile(rotatedPath, []byte("[2026-06-15 10:00:00] old\n"), 0o644); err != nil {
		t.Fatalf("write rotated log: %v", err)
	}
	cursor, errCursor := newLogCursor(rotatedPath, int64(len("[2026-06-15 10:00:00] old\n")), 0)
	if errCursor != nil {
		t.Fatalf("newLogCursor() error = %v", errCursor)
	}
	if errRemove := os.Remove(rotatedPath); errRemove != nil {
		t.Fatalf("remove rotated log: %v", errRemove)
	}

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(cursor)+"&limit=1")
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true")
	}
	if !reflect.DeepEqual(resp.Lines, []string{current}) {
		t.Fatalf("lines = %#v, want current tail", resp.Lines)
	}
}

func TestGetLogsMissingLogDirKeepsOKEmptyResponse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape("not-base64")+"&limit=1")
	if len(resp.Lines) != 0 {
		t.Fatalf("lines = %#v, want empty", resp.Lines)
	}
	if resp.LineCount != 0 {
		t.Fatalf("line-count = %d, want 0", resp.LineCount)
	}
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true for cursor against missing log dir")
	}
}

func TestGetLogsLoggingDisabledKeepsBadRequest(t *testing.T) {
	status, body := performGetLogsRaw(t, newLogsTestHandler(t.TempDir(), false), "/v0/management/logs?cursor=not-base64&limit=1")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if !strings.Contains(body, "logging to file disabled") {
		t.Fatalf("body = %s, want logging disabled error", body)
	}
}

func mustEncodeRawCursor(t *testing.T, cursor logCursor) string {
	t.Helper()
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatalf("json.Marshal cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type logsAPIResponse struct {
	Lines           []string `json:"lines"`
	LineCount       int      `json:"line-count"`
	LatestTimestamp int64    `json:"latest-timestamp"`
	NextCursor      string   `json:"next-cursor"`
	CursorReset     bool     `json:"cursor-reset"`
}

func newLogsTestHandler(dir string, loggingToFile bool) *Handler {
	h := NewHandlerWithoutConfigFilePath(&config.Config{LoggingToFile: loggingToFile}, nil)
	h.SetLogDirectory(dir)
	return h
}

func performGetLogs(t *testing.T, h *Handler, target string) logsAPIResponse {
	t.Helper()
	status, body := performGetLogsRaw(t, h, target)
	if status != http.StatusOK {
		t.Fatalf("GetLogs status = %d, body = %s", status, body)
	}
	var resp logsAPIResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Lines == nil {
		resp.Lines = []string{}
	}
	return resp
}

func performGetLogsRaw(t *testing.T, h *Handler, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h.GetLogs(c)
	return rec.Code, rec.Body.String()
}

func writeMainLog(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, defaultLogFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write main log: %v", err)
	}
}

func appendMainLog(t *testing.T, dir, content string) {
	t.Helper()
	file, errOpen := os.OpenFile(filepath.Join(dir, defaultLogFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if errOpen != nil {
		t.Fatalf("open main log: %v", errOpen)
	}
	if _, errWrite := file.WriteString(content); errWrite != nil {
		_ = file.Close()
		t.Fatalf("append main log: %v", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("close main log: %v", errClose)
	}
}

func TestGetRequestErrorLogsSupportsPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(logDir, fmt.Sprintf("error-%d.log", i))
		if err := os.WriteFile(name, []byte("log"), 0o644); err != nil {
			t.Fatalf("write log file: %v", err)
		}
		modified := time.Unix(int64(i+1), 0)
		if err := os.Chtimes(name, modified, modified); err != nil {
			t.Fatalf("set log mtime: %v", err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(logDir)
	status, body := performRequestLogListRaw(t, h, "/v0/management/request-error-logs?page=2&page_size=2")
	if status != http.StatusOK {
		t.Fatalf("GetRequestErrorLogs status = %d, body = %s", status, body)
	}

	var response struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
		Page       int `json:"page"`
		PageSize   int `json:"page_size"`
		Total      int `json:"total"`
		TotalPages int `json:"total_pages"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := []string{response.Files[0].Name, response.Files[1].Name}; !reflect.DeepEqual(got, []string{"error-2.log", "error-1.log"}) {
		t.Fatalf("files = %#v, want page 2 in descending mtime order", got)
	}
	if response.Page != 2 || response.PageSize != 2 || response.Total != 5 || response.TotalPages != 3 {
		t.Fatalf("pagination = page %d size %d total %d pages %d, want 2/2/5/3", response.Page, response.PageSize, response.Total, response.TotalPages)
	}
}

func TestGetRequestSuccessLogsClampsPageAndRejectsInvalidPageSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	for i := 0; i < 2; i++ {
		name := filepath.Join(logDir, fmt.Sprintf("success-%d.log", i))
		if err := os.WriteFile(name, []byte("log"), 0o644); err != nil {
			t.Fatalf("write log file: %v", err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{SDKConfig: config.SDKConfig{RequestLog: true, SuccessRequestLog: true}}, nil)
	h.SetLogDirectory(logDir)
	status, body := performRequestLogListRaw(t, h, "/v0/management/request-success-logs?page=99&page_size=1")
	if status != http.StatusOK {
		t.Fatalf("GetRequestSuccessLogs status = %d, body = %s", status, body)
	}
	var response struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
		Files      []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Page != 2 || response.TotalPages != 2 || len(response.Files) != 1 {
		t.Fatalf("clamped response = page %d pages %d files %d, want 2/2/1", response.Page, response.TotalPages, len(response.Files))
	}

	status, body = performRequestLogListRaw(t, h, "/v0/management/request-success-logs?page_size=0")
	if status != http.StatusBadRequest {
		t.Fatalf("invalid page_size status = %d, body = %s", status, body)
	}
}

func TestGetRequestErrorLogsKeepsLegacyResponseWithoutPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	name := filepath.Join(logDir, "error-legacy.log")
	if err := os.WriteFile(name, []byte("log"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(logDir)
	status, body := performRequestLogListRaw(t, h, "/v0/management/request-error-logs")
	if status != http.StatusOK {
		t.Fatalf("GetRequestErrorLogs status = %d, body = %s", status, body)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response) != 1 {
		t.Fatalf("legacy response fields = %d, want only files: %s", len(response), body)
	}
	if _, ok := response["files"]; !ok {
		t.Fatalf("legacy response missing files: %s", body)
	}
}

func performRequestLogListRaw(t *testing.T, h *Handler, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if strings.Contains(target, "request-success-logs") {
		h.GetRequestSuccessLogs(c)
	} else {
		h.GetRequestErrorLogs(c)
	}
	return rec.Code, rec.Body.String()
}

func TestRequestLogModelFromName(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		want   string
	}{
		{name: "success-gpt-5.6-sol-017be4c8.log", prefix: "success-", want: "gpt-5.6-sol"},
		{name: "error-glm-5.2-0cd29872.log", prefix: "error-", want: "glm-5.2"},
		{name: "error-unknown-model-1.log", prefix: "error-", want: "unknown-model"},
		{name: "success-only.log", prefix: "success-", want: "only"},
		{name: "error-bad.txt", prefix: "error-", want: ""},
		{name: "success-gpt-5.log", prefix: "error-", want: ""},
	}
	for _, tc := range cases {
		if got := requestLogModelFromName(tc.name, tc.prefix); got != tc.want {
			t.Fatalf("requestLogModelFromName(%q, %q) = %q, want %q", tc.name, tc.prefix, got, tc.want)
		}
	}
}

func TestFilterRequestLogFilesByModelAndTime(t *testing.T) {
	files := []requestLogFile{
		{Name: "success-gpt-5.6-sol-a1.log", Modified: 100},
		{Name: "success-glm-5.2-b2.log", Modified: 200},
		{Name: "success-gpt-4o-c3.log", Modified: 300},
		{Name: "success-claude-opus-d4.log", Modified: 400},
	}

	byModel := filterRequestLogFiles(files, "success-", requestLogFilter{model: "GPT"})
	if got := []string{byModel[0].Name, byModel[1].Name}; !reflect.DeepEqual(got, []string{"success-gpt-5.6-sol-a1.log", "success-gpt-4o-c3.log"}) {
		t.Fatalf("model filter = %#v", got)
	}

	byTime := filterRequestLogFiles(files, "success-", requestLogFilter{from: 200, to: 300})
	if len(byTime) != 2 || byTime[0].Name != "success-glm-5.2-b2.log" || byTime[1].Name != "success-gpt-4o-c3.log" {
		t.Fatalf("time filter = %#v", byTime)
	}

	combined := filterRequestLogFiles(files, "success-", requestLogFilter{model: "gpt", from: 250, to: 350})
	if len(combined) != 1 || combined[0].Name != "success-gpt-4o-c3.log" {
		t.Fatalf("combined filter = %#v", combined)
	}
}

func TestGetRequestSuccessLogsFiltersByModelAndTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logDir := t.TempDir()
	entries := []struct {
		name string
		mod  int64
	}{
		{"success-gpt-5.6-sol-a1.log", 100},
		{"success-glm-5.2-b2.log", 200},
		{"success-gpt-4o-c3.log", 300},
		{"success-claude-opus-d4.log", 400},
	}
	for _, entry := range entries {
		path := filepath.Join(logDir, entry.name)
		if err := os.WriteFile(path, []byte("log"), 0o644); err != nil {
			t.Fatalf("write log file: %v", err)
		}
		modified := time.Unix(entry.mod, 0)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatalf("set log mtime: %v", err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{SDKConfig: config.SDKConfig{RequestLog: true, SuccessRequestLog: true}}, nil)
	h.SetLogDirectory(logDir)

	status, body := performRequestLogListRaw(t, h, "/v0/management/request-success-logs?page=1&page_size=10&model=gpt&from=150&to=350")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var response struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Total != 1 || len(response.Files) != 1 || response.Files[0].Name != "success-gpt-4o-c3.log" {
		t.Fatalf("filtered response = total %d files %#v", response.Total, response.Files)
	}
}

func TestGetRequestErrorLogsRejectsInvalidTimeFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetLogDirectory(t.TempDir())

	status, body := performRequestLogListRaw(t, h, "/v0/management/request-error-logs?from=not-a-time")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, "from") {
		t.Fatalf("expected from validation error, body = %s", body)
	}

	status, body = performRequestLogListRaw(t, h, "/v0/management/request-error-logs?from=200&to=100")
	if status != http.StatusBadRequest {
		t.Fatalf("from>to status = %d, body = %s", status, body)
	}
}
