package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServerMgmtAPIReturnedFormat guards that the model-list probe format string
// keeps its status-code and body placeholders (a %d→wrong-verb edit would drop
// them).
func TestServerMgmtAPIReturnedFormat(t *testing.T) {
	msg := fmt.Sprintf(errMgmtAPIReturned, 404, "boom-body")
	assertKoreanError(t, "errMgmtAPIReturned", msg)
	if !strings.Contains(msg, "404") || !strings.Contains(msg, "boom-body") {
		t.Fatalf("형식 문자열이 상태 코드와 본문을 보존해야 합니다: %q", msg)
	}
}

// TestServerMgmtSkillExistsSentinel guards the cross-stack contract: the server's
// duplicate-skill message must carry the "이미 존재" marker that
// web/src/app/(main)/system/skills/page.tsx greps (msg.includes("이미 존재")) to
// switch into the overwrite-confirm flow. Drift here silently disables overwrite.
func TestServerMgmtSkillExistsSentinel(t *testing.T) {
	msg := errMgmtSkillExistsPre + "my-skill" + errMgmtSkillExistsPost
	assertKoreanError(t, "skillExists", msg)
	if !strings.Contains(msg, "이미 존재") {
		t.Fatalf("프론트 미러 마커 '이미 존재' 가 없습니다: %q", msg)
	}
	if !strings.Contains(msg, "my-skill") {
		t.Fatalf("스킬 이름이 메시지에 포함돼야 합니다: %q", msg)
	}
}

// TestServerMgmtPGGate503Localized exercises the real shared DB gate every
// management handler passes through: with no PostgreSQL handle, pg() writes 503
// and returns nil. The 503 body must be Korean.
func TestServerMgmtPGGate503Localized(t *testing.T) {
	s := &Server{m: &Manager{}} // pg == nil
	rec := httptest.NewRecorder()
	if got := s.pg(rec); got != nil {
		t.Fatal("pg() 는 DB 핸들이 없으면 nil 을 반환해야 합니다")
	}
	if rec.Code != 503 {
		t.Fatalf("상태 코드 503 을 기대했으나 %d", rec.Code)
	}
	assertKoreanError(t, "pg.503", decodeErrorField(t, rec.Body.Bytes()))
}

// TestServerMgmtDeleteTaskBadIDLocalized drives pgDeleteTask through its first
// guard (non-numeric id → 400) without any DB or engine.
func TestServerMgmtDeleteTaskBadIDLocalized(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	s.pgDeleteTask(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("상태 코드 400 을 기대했으나 %d (본문 %s)", rec.Code, rec.Body.Bytes())
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	assertKoreanError(t, "deleteTask.badID", got)
	if got != errMgmtTaskIDInvalid {
		t.Fatalf("errMgmtTaskIDInvalid 와 일치해야 합니다: %q", got)
	}
}

// TestServerMgmtListModelsNoKeyLocalized drives pgListModels with no api key and
// no profile id, so it returns {"ok":false,"error":...} before touching the DB.
func TestServerMgmtListModelsNoKeyLocalized(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/llm/models", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	s.pgListModels(rec, req)
	if rec.Code != 200 {
		t.Fatalf("상태 코드 200 을 기대했으나 %d", rec.Code)
	}
	got := decodeErrorField(t, rec.Body.Bytes())
	assertKoreanError(t, "listModels.noKey", got)
	if got != errMgmtNoAPIKey {
		t.Fatalf("errMgmtNoAPIKey 와 일치해야 합니다: %q", got)
	}
}
