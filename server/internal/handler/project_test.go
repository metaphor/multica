package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateProjectSettings(t *testing.T) {
	ctx := context.Background()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Settings test project",
	})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var project ProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
		t.Fatalf("decode created project: %v", err)
	}
	t.Cleanup(func() {
		delW := httptest.NewRecorder()
		delReq := newRequest("DELETE", "/api/projects/"+project.ID, nil)
		delReq = withURLParam(delReq, "id", project.ID)
		testHandler.DeleteProject(delW, delReq)
	})

	t.Run("accepts valid agent_workdir", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": true,
				"agent_workdir":        "src/app",
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for valid agent_workdir, got %d: %s", w.Code, w.Body.String())
		}
		var updated ProjectResponse
		if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated project: %v", err)
		}
	})

	t.Run("accepts settings with enable_agent_workdir false", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": false,
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 when enable_agent_workdir is false, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects agent_workdir with .. traversal", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": true,
				"agent_workdir":        "../etc",
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for .. traversal, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects agent_workdir equal to dot", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": true,
				"agent_workdir":        ".",
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for '.' agent_workdir, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects absolute agent_workdir path", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": true,
				"agent_workdir":        "/etc/passwd",
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for absolute path, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("rejects empty agent_workdir when enabled", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": true,
				"agent_workdir":        "",
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for empty agent_workdir, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("settings round-trip preserves JSON", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": true,
				"agent_workdir":        "packages",
			},
		})
		req = withURLParam(req, "id", project.ID)
		testHandler.UpdateProject(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var updated ProjectResponse
		if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated project: %v", err)
		}

		// Read back from DB directly to confirm persistence
		var raw []byte
		if err := testPool.QueryRow(ctx, `SELECT settings FROM project WHERE id = $1`, project.ID).Scan(&raw); err != nil {
			t.Fatalf("read settings from DB: %v", err)
		}
		var stored map[string]any
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatalf("decode stored settings: %v", err)
		}
		if stored["agent_workdir"] != "packages" {
			t.Fatalf("expected agent_workdir='packages', got %v", stored["agent_workdir"])
		}
	})

	t.Run("clears settings by sending null", func(t *testing.T) {
		// First set settings
		w1 := httptest.NewRecorder()
		req1 := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": map[string]any{
				"enable_agent_workdir": false,
			},
		})
		req1 = withURLParam(req1, "id", project.ID)
		testHandler.UpdateProject(w1, req1)

		// Now clear via null
		w2 := httptest.NewRecorder()
		req2 := newRequest("PATCH", "/api/projects/"+project.ID, map[string]any{
			"settings": nil,
		})
		req2 = withURLParam(req2, "id", project.ID)
		testHandler.UpdateProject(w2, req2)
		if w2.Code != http.StatusOK {
			t.Fatalf("expected 200 clearing settings, got %d: %s", w2.Code, w2.Body.String())
		}
		var updated ProjectResponse
		if err := json.NewDecoder(w2.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated project: %v", err)
		}
		settingsMap, _ := updated.Settings.(map[string]any)
		if settingsMap == nil || len(settingsMap) > 0 {
			t.Fatalf("expected empty settings after clear, got %v", updated.Settings)
		}
	})
}
