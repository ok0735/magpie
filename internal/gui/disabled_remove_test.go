package gui

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/provider"
)

// The provider editor's right-click "Remove from the list": one model is added
// to the disabled list (Settings › Models › Disabled models, #1867) and leaves
// the provider's own list with it — the list the user reads to see what a
// provider has, and where a hundred models a handful of which answer are
// unreadable (朱, 2026-10-16: NVIDIA). Settings is where one comes back.
func TestRemoveModelFromTheList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	// two providers share a bare id, as relays do: removing one leaves the
	// other's alone
	for _, id := range []string{"fake", "other"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:9/v1",
			Models: []string{"m1", "m2"}}); err != nil {
			t.Fatal(err)
		}
	}
	type state struct {
		DisabledModels []string            `json:"disabledModels"`
		DisabledHits   map[string][]string `json:"disabledHits"`
	}
	post := func(body string) (int, state) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings/disabled-models/add", strings.NewReader(body)))
		var out state
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// the provider's own list, as the editor is handed it
	listed := func(id string) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/api/providers", nil))
		var out struct {
			Providers []struct {
				ID     string `json:"id"`
				Models []struct {
					ID string `json:"id"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, p := range out.Providers {
			if p.ID != id {
				continue
			}
			for _, m := range p.Models {
				ids = append(ids, m.ID)
			}
		}
		slices.Sort(ids)
		return ids
	}
	if got := listed("fake"); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("before: %v", got)
	}
	// one click in the menu: the model it names
	code, out := post(`{"models":["fake/m2"]}`)
	if code != 200 || !slices.Equal(out.DisabledModels, []string{"fake/m2"}) || !slices.Equal(out.DisabledHits["fake/m2"], []string{"fake/m2"}) {
		t.Fatalf("%d %+v", code, out)
	}
	// it is gone from that provider's list, and the other one's stands
	if got := listed("fake"); !slices.Equal(got, []string{"m1"}) {
		t.Fatalf("after: %v", got)
	}
	if got := listed("other"); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("the other provider lost a model of its own: %v", got)
	}
	// the same model again: one pattern, said once
	if code, out := post(`{"models":["fake/m2"]}`); code != 200 || !slices.Equal(out.DisabledModels, []string{"fake/m2"}) {
		t.Fatalf("twice: %d %+v", code, out)
	}
	// nothing named is a request that says nothing: refused, and the list as
	// it was (a request that says nothing must not clear it)
	if code, _ := post(`{"models":[]}`); code != 400 {
		t.Fatalf("empty: %d", code)
	}
	if got := listed("fake"); !slices.Equal(got, []string{"m1"}) {
		t.Fatalf("an empty request changed the list: %v", got)
	}
	// and it is back the way Settings brings one back
	if _, err := provider.EnableModels("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if got := listed("fake"); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("enabled: %v", got)
	}
}
