package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const (
	abiVersion uint32 = 1
	pluginID          = "ag-autoban"
	providerName      = "antigravity"
)

var pluginVersion = "0.1.0"

// ─── State ────────────────────────────────────────────────────────────────────

var store = newBanStore()

type banStore struct {
	mu          sync.Mutex
	state       banState
	statePath   string
	authDir     string
	loaded      bool
}

type banState struct {
	Bans    map[string]*banEntry    `json:"bans"`
	Invalids map[string]*invalidEntry `json:"invalids"`
}

type banEntry struct {
	Reason    string `json:"reason"`
	BannedAt  int64  `json:"banned_at"`
	ResetAt   int64  `json:"reset_at"`
	Active    bool   `json:"active"`
	StatusCode int   `json:"status_code"`
}

type invalidEntry struct {
	Reason        string `json:"reason"`
	BannedAt      int64  `json:"banned_at"`
	AuthFileMTime int64  `json:"auth_file_mtime"`
	Active        bool   `json:"active"`
	StatusCode    int    `json:"status_code"`
}

func newBanStore() *banStore {
	s := &banStore{}
	s.state.Bans = make(map[string]*banEntry)
	s.state.Invalids = make(map[string]*invalidEntry)
	s.statePath = defaultStatePath()
	s.authDir = defaultAuthDir()
	return s
}

func defaultStatePath() string {
	dir := os.Getenv("AG_AUTOBAN_DIR")
	if dir == "" {
		dir = "/root/.cli-proxy-api/plugins/ag-autoban"
	}
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "state.json")
}

func defaultAuthDir() string {
	dir := os.Getenv("AG_AUTOBAN_AUTH_DIR")
	if dir == "" {
		dir = "/root/cliproxyapi/auth"
	}
	return dir
}

func (s *banStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}
	var st banState
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	if st.Bans == nil {
		st.Bans = make(map[string]*banEntry)
	}
	if st.Invalids == nil {
		st.Invalids = make(map[string]*invalidEntry)
	}
	s.state = st
}

func (s *banStore) save() {
	data, _ := json.MarshalIndent(s.state, "", "  ")
	_ = os.WriteFile(s.statePath, data, 0644)
}

// ─── Ban management ───────────────────────────────────────────────────────────

func (s *banStore) addBan(authKey, reason string, statusCode int, resetAt int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	entry := &banEntry{
		Reason:     reason,
		BannedAt:   now,
		ResetAt:    resetAt,
		Active:     true,
		StatusCode: statusCode,
	}
	s.state.Bans[authKey] = entry
	s.save()
}

func (s *banStore) addInvalid(authKey, reason string, statusCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	mtime := s.authFileMTime(authKey)
	entry := &invalidEntry{
		Reason:        reason,
		BannedAt:      now,
		AuthFileMTime: mtime,
		Active:        true,
		StatusCode:    statusCode,
	}
	s.state.Invalids[authKey] = entry
	s.save()
}

func (s *banStore) clearBan(authKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if e, ok := s.state.Bans[authKey]; ok && e.Active {
		e.Active = false
		changed = true
	}
	if e, ok := s.state.Invalids[authKey]; ok && e.Active {
		e.Active = false
		changed = true
	}
	if changed {
		s.save()
	}
}

func (s *banStore) authFileMTime(authKey string) int64 {
	if s.authDir == "" || authKey == "" {
		return 0
	}
	info, err := os.Stat(filepath.Join(s.authDir, authKey))
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

// expireAndReconcile removes expired bans and reconciles invalid auths whose
// files have been replaced. Must be called with mu held.
func (s *banStore) expireAndReconcile() {
	now := time.Now().Unix()
	changed := false
	for _, entry := range s.state.Bans {
		if entry.Active && entry.ResetAt > 0 && entry.ResetAt <= now {
			entry.Active = false
			changed = true
		}
	}
	for key, entry := range s.state.Invalids {
		if !entry.Active {
			continue
		}
		currentMTime := s.authFileMTime(key)
		if currentMTime > entry.AuthFileMTime {
			entry.Active = false
			changed = true
		}
	}
	if changed {
		s.save()
	}
}

func (s *banStore) isActiveBan(authKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAndReconcile()
	if e, ok := s.state.Bans[authKey]; ok && e.Active {
		return true
	}
	if e, ok := s.state.Invalids[authKey]; ok && e.Active {
		return true
	}
	return false
}

func (s *banStore) activeBans() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAndReconcile()
	var keys []string
	for key, e := range s.state.Bans {
		if e.Active {
			keys = append(keys, key)
		}
	}
	for key, e := range s.state.Invalids {
		if e.Active {
			keys = append(keys, key)
		}
	}
	return keys
}

func (s *banStore) releaseAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, e := range s.state.Bans {
		if e.Active {
			e.Active = false
			count++
		}
	}
	for _, e := range s.state.Invalids {
		if e.Active {
			e.Active = false
			count++
		}
	}
	if count > 0 {
		s.save()
	}
	return count
}

func (s *banStore) releaseOne(authKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if e, ok := s.state.Bans[authKey]; ok && e.Active {
		e.Active = false
		changed = true
	}
	if e, ok := s.state.Invalids[authKey]; ok && e.Active {
		e.Active = false
		changed = true
	}
	if changed {
		s.save()
	}
	return changed
}

func (s *banStore) snapshot() banState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAndReconcile()
	cp := banState{
		Bans:    make(map[string]*banEntry, len(s.state.Bans)),
		Invalids: make(map[string]*invalidEntry, len(s.state.Invalids)),
	}
	for k, v := range s.state.Bans {
		cp.Bans[k] = v
	}
	for k, v := range s.state.Invalids {
		cp.Invalids[k] = v
	}
	return cp
}

// ─── Auth key extraction ──────────────────────────────────────────────────────

var reAntigravityFile = regexp.MustCompile(`antigravity-[^\s/]+\.json`)

func extractAuthKey(values ...string) string {
	for _, v := range values {
		if v == "" {
			continue
		}
		if m := reAntigravityFile.FindString(v); m != "" {
			return m
		}
	}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func authKeyFromUsageRecord(rec usageRecord) string {
	return extractAuthKey(rec.AuthFile, rec.Source, rec.AuthID, rec.AuthIndex)
}

func authKeyFromCandidate(candidate schedulerAuthCandidate) string {
	authFile := firstNonEmpty(
		candidate.Attributes["auth_file"],
		candidate.Attributes["path"],
		candidate.Attributes["file"],
	)
	metaFile := firstNonEmpty(
		stringFromAny(candidate.Metadata["auth_file"]),
		stringFromAny(candidate.Metadata["path"]),
		stringFromAny(candidate.Metadata["file"]),
	)
	return extractAuthKey(authFile, metaFile, candidate.ID,
		candidate.Attributes["source"], stringFromAny(candidate.Metadata["source"]),
		candidate.Attributes["auth_index"], stringFromAny(candidate.Metadata["auth_index"]))
}

// ─── 429 body parsing ─────────────────────────────────────────────────────────

var reResetsIn = regexp.MustCompile(`Resets in (\d+)h(\d+)m(\d+)s`)

func parseResetAt(body string, now int64) int64 {
	m := reResetsIn.FindStringSubmatch(body)
	if m == nil {
		return now + 24*3600
	}
	h, mn, s := parseInt64(m[1]), parseInt64(m[2]), parseInt64(m[3])
	return now + h*3600 + mn*60 + s
}

// ─── Types (same as codex-token-usage) ────────────────────────────────────────

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type pluginRegisterResponse struct {
	SchemaVersion int            `json:"schema_version"`
	Metadata      pluginMetadata `json:"metadata"`
	Capabilities  capabilities   `json:"capabilities"`
}

type pluginMetadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type capabilities struct {
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
	Scheduler     bool `json:"scheduler"`
}

type lifecycleRequest struct {
	ConfigYAML json.RawMessage `json:"config_yaml"`
}

type managementRegistrationResponse struct {
	Routes []managementRoute `json:"routes,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type managementRequest struct {
	Method  string              `json:"Method"`
	Path    string              `json:"Path"`
	Headers map[string][]string `json:"Headers"`
	Query   map[string][]string `json:"Query"`
	Body    []byte              `json:"Body"`
}

type managementResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"`
}

type schedulerPickRequest struct {
	Provider   string                   `json:"Provider"`
	Providers  []string                 `json:"Providers"`
	Model      string                   `json:"Model"`
	Stream     bool                     `json:"Stream"`
	Options    schedulerOptions         `json:"Options"`
	Candidates []schedulerAuthCandidate `json:"Candidates"`
}

type schedulerOptions struct {
	Headers  map[string][]string `json:"Headers"`
	Metadata map[string]any      `json:"Metadata"`
}

type schedulerAuthCandidate struct {
	ID         string            `json:"ID"`
	Provider   string            `json:"Provider"`
	Priority   int               `json:"Priority"`
	Status     string            `json:"Status"`
	Attributes map[string]string `json:"Attributes"`
	Metadata   map[string]any    `json:"Metadata"`
}

type schedulerPickResponse struct {
	AuthID          string `json:"AuthID"`
	DelegateBuiltin string `json:"DelegateBuiltin"`
	Handled         bool   `json:"Handled"`
}

type schedulerRejectError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *schedulerRejectError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type usageRecord struct {
	Provider        string              `json:"Provider"`
	Model           string              `json:"Model"`
	AuthID          string              `json:"AuthID"`
	AuthIndex       string              `json:"AuthIndex"`
	AuthFile        string              `json:"AuthFile"`
	Source          string              `json:"Source"`
	Failed          bool                `json:"Failed"`
	Failure         usageFailure        `json:"Failure"`
	ResponseHeaders map[string][]string `json:"ResponseHeaders"`
}

type usageFailure struct {
	StatusCode int    `json:"StatusCode"`
	Body       string `json:"Body"`
}

// ─── C ABI exports ────────────────────────────────────────────────────────────

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	_ = host
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (ret C.int) {
	defer func() {
		if r := recover(); r != nil {
			ret = 1
			writeResponse(response, errorEnvelope("panic", fmt.Sprintf("%v", r)))
		}
	}()
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	methodStr := C.GoString(method)
	var req []byte
	if request != nil && requestLen > 0 {
		req = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(methodStr, req)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
}

// ─── Method dispatch ──────────────────────────────────────────────────────────

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return okJSON(pluginRegisterResponse{
			SchemaVersion: 1,
			Metadata: pluginMetadata{
				Name:             "AG Autoban",
				Version:          pluginVersion,
				Author:           "neronet",
				GitHubRepository: "https://github.com/yogaraihan60/ag-autoban",
				Logo:             "",
				ConfigFields:     []configField{},
			},
			Capabilities: capabilities{
				UsagePlugin:   true,
				ManagementAPI: true,
				Scheduler:     true,
			},
		}), nil

	case "management.register":
		return okJSON(managementRegistrationResponse{
			Routes: []managementRoute{
				{Method: "GET", Path: "/plugins/ag-autoban/status", Description: "Current autoban state JSON."},
				{Method: "POST", Path: "/plugins/ag-autoban/release", Description: "Release active autobans (all or selected)."},
			},
		}), nil

	case "management.handle":
		var req managementRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return okJSON(jsonResponse(http.StatusBadRequest, map[string]any{"error": "bad_request", "message": err.Error()})), nil
		}
		return okJSON(handleManagement(req)), nil

	case "usage.handle":
		var rec usageRecord
		if err := json.Unmarshal(request, &rec); err != nil {
			return okJSON(map[string]any{"ignored": true, "error": err.Error()}), nil
		}
		return handleUsage(rec), nil

	case "scheduler.pick":
		var req schedulerPickRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return okJSON(schedulerPickResponse{Handled: false}), nil
		}
		return handleSchedulerPick(req)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// ─── usage.handle ─────────────────────────────────────────────────────────────

func handleUsage(rec usageRecord) []byte {
	if !strings.EqualFold(strings.TrimSpace(rec.Provider), providerName) {
		return okJSON(map[string]any{"ignored": true})
	}

	authKey := authKeyFromUsageRecord(rec)
	if authKey == "" {
		return okJSON(map[string]any{"ignored": true, "reason": "no auth_key"})
	}

	status := rec.Failure.StatusCode
	if rec.Failed && status == 0 {
		status = 599
	}

	if !rec.Failed && status >= 200 && status < 400 {
		store.clearBan(authKey)
		return okJSON(map[string]any{"stored": true, "action": "cleared"})
	}

	if !rec.Failed {
		return okJSON(map[string]any{"stored": true, "action": "noop"})
	}

	now := time.Now().Unix()

	if status == http.StatusTooManyRequests {
		resetAt := parseResetAt(rec.Failure.Body, now)
		store.addBan(authKey, "429 quota exceeded", status, resetAt)
		return okJSON(map[string]any{"stored": true, "action": "banned", "reset_at": resetAt})
	}

	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		bodyLower := strings.ToLower(rec.Failure.Body)
		if strings.Contains(bodyLower, "invalid_grant") || strings.Contains(bodyLower, "invalid_token") {
			store.addInvalid(authKey, fmt.Sprintf("%d invalid_grant", status), status)
			return okJSON(map[string]any{"stored": true, "action": "invalid_auth"})
		}
		store.addInvalid(authKey, fmt.Sprintf("%d auth error", status), status)
		return okJSON(map[string]any{"stored": true, "action": "invalid_auth"})
	}

	return okJSON(map[string]any{"stored": true, "action": "noop", "status": status})
}

// ─── scheduler.pick ───────────────────────────────────────────────────────────

func handleSchedulerPick(req schedulerPickRequest) ([]byte, error) {
	if !isAntigravitySchedulerRequest(req) {
		return okJSON(schedulerPickResponse{Handled: false}), nil
	}
	if len(req.Candidates) == 0 {
		return okJSON(schedulerPickResponse{Handled: false}), nil
	}

	store.load()

	activeBans := store.activeBans()
	if len(activeBans) == 0 {
		return okJSON(schedulerPickResponse{Handled: false}), nil
	}

	banSet := make(map[string]bool, len(activeBans))
	for _, k := range activeBans {
		banSet[k] = true
	}

	available := make([]schedulerAuthCandidate, 0, len(req.Candidates))
	filtered := 0
	for _, candidate := range req.Candidates {
		if !strings.EqualFold(candidate.Provider, providerName) {
			available = append(available, candidate)
			continue
		}
		authKey := authKeyFromCandidate(candidate)
		if authKey != "" && banSet[authKey] {
			filtered++
			continue
		}
		available = append(available, candidate)
	}

	if filtered == 0 {
		return okJSON(schedulerPickResponse{Handled: false}), nil
	}

	if len(available) == 0 {
		return errorEnvelopeWithStatus("auth_unavailable",
			"all antigravity candidates are auto-banned",
			http.StatusServiceUnavailable), nil
	}

	chosen := available[0]
	for _, c := range available[1:] {
		if c.Priority > chosen.Priority {
			chosen = c
		}
	}
	return okJSON(schedulerPickResponse{AuthID: chosen.ID, Handled: true}), nil
}

func isAntigravitySchedulerRequest(req schedulerPickRequest) bool {
	if strings.EqualFold(strings.TrimSpace(req.Provider), providerName) {
		return true
	}
	if len(req.Providers) == 1 && strings.EqualFold(strings.TrimSpace(req.Providers[0]), providerName) {
		return true
	}
	for _, p := range req.Providers {
		if strings.EqualFold(strings.TrimSpace(p), providerName) {
			return true
		}
	}
	return false
}

// ─── management.handle ────────────────────────────────────────────────────────

func handleManagement(req managementRequest) managementResponse {
	if strings.HasPrefix(req.Path, "/v0/management/plugins/"+pluginID+"/status") {
		st := store.snapshot()
		data, _ := json.Marshal(map[string]any{
			"bans":    st.Bans,
			"invalids": st.Invalids,
		})
		return managementResponse{
			StatusCode: http.StatusOK,
			Headers:    map[string][]string{"content-type": {"application/json"}},
			Body:       data,
		}
	}
	if strings.HasPrefix(req.Path, "/v0/management/plugins/"+pluginID+"/release") {
		if !strings.EqualFold(req.Method, http.MethodPost) {
			return jsonResponse(http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		}
		var body struct {
			Scope string   `json:"scope"`
			Items []string `json:"items"`
		}
		if len(req.Body) > 0 {
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return jsonResponse(http.StatusBadRequest, map[string]any{"error": "bad_request", "message": err.Error()})
			}
		}
		if body.Scope == "" {
			body.Scope = "all"
		}
		if body.Scope == "all" {
			n := store.releaseAll()
			return jsonResponse(http.StatusOK, map[string]any{"released": n})
		}
		if body.Scope == "selected" {
			released := 0
			for _, key := range body.Items {
				if store.releaseOne(key) {
					released++
				}
			}
			return jsonResponse(http.StatusOK, map[string]any{"released": released})
		}
		return jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid_scope"})
	}
	return jsonResponse(http.StatusNotFound, map[string]any{"error": "not_found"})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func stringFromAny(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case fmt.Stringer:
		return val.String()
	default:
		b, _ := json.Marshal(v)
		return strings.Trim(string(b), `"`)
	}
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func okJSON(v any) []byte {
	data, _ := json.Marshal(v)
	wrapped, _ := json.Marshal(envelope{
		OK:     true,
		Result: json.RawMessage(data),
	})
	return wrapped
}

func errorEnvelope(code, message string) []byte {
	data, _ := json.Marshal(envelope{
		OK:    false,
		Error: &envelopeError{Code: code, Message: message},
	})
	return data
}

func errorEnvelopeWithStatus(code, message string, httpStatus int) []byte {
	data, _ := json.Marshal(envelope{
		OK:    false,
		Error: &envelopeError{Code: code, Message: message, HTTPStatus: httpStatus},
	})
	return data
}

func jsonResponse(status int, v any) managementResponse {
	body, _ := json.Marshal(v)
	return managementResponse{
		StatusCode: status,
		Headers:    map[string][]string{"content-type": {"application/json"}},
		Body:       body,
	}
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
