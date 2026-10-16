package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// The chat window beside the Providers page (agentchat.go): a paste of a
// model name, a base URL and a key finds the provider that URL names or adds
// it, fetches the vendor's list, sends every model the smallest request a
// test sends, and keeps the ones that answered — the models that list a name
// but can't be called are taken out of its picks.

// newAgentHome keeps a test's providers off the user's own files.
func newAgentHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// agentVendor is a fake vendor: its /v1/models list, and a chat endpoint that
// answers every model but the one called broken.
type agentVendor struct {
	*httptest.Server
	mu     sync.Mutex
	broken string
	asked  []string
}

func newAgentVendor(t *testing.T, models []string, broken string) *agentVendor {
	t.Helper()
	v := &agentVendor{broken: broken}
	v.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.asked = append(v.asked, r.Method+" "+r.URL.Path)
		v.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/models":
			var list []map[string]any
			for _, m := range models {
				list = append(list, map[string]any{"id": m, "object": "model"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
		case r.URL.Path == "/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(body, &in)
			if in.Model == v.broken {
				w.WriteHeader(404)
				io.WriteString(w, `{"error":{"message":"the model does not exist"}}`)
				return
			}
			io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(v.Close)
	return v
}

// breakModel makes a model unrunnable from now on.
func (v *agentVendor) breakModel(m string) {
	v.mu.Lock()
	v.broken = m
	v.mu.Unlock()
}

// chat runs one message through the route and reads the events back.
func chat(t *testing.T, mux *http.ServeMux, body map[string]any) []agentEvent {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/agent/chat", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatalf("chat: %d %s", w.Code, w.Body.String())
	}
	var events []agentEvent
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev agentEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Fatalf("the run didn't finish: %v", events)
	}
	return events
}

// event is the first event of that type, nil when there is none.
func event(events []agentEvent, kind string) *agentEvent {
	for i := range events {
		if events[i].Type == kind {
			return &events[i]
		}
	}
	return nil
}

func problems(events []agentEvent) []string {
	var out []string
	for _, ev := range events {
		if ev.Type == "problem" {
			out = append(out, ev.Code)
		}
	}
	return out
}

// eventOfCode is the problem event carrying that code, nil when there is none.
func eventOfCode(events []agentEvent, code string) *agentEvent {
	for i := range events {
		if events[i].Type == "problem" && events[i].Code == code {
			return &events[i]
		}
	}
	return nil
}

func TestAgentChatParse(t *testing.T) {
	for _, c := range []struct {
		name   string
		text   string
		base   string
		key    string
		models []string
		pname  string
	}{
		{
			name:   "a model, a URL and a key on their own lines",
			text:   "gpt-5.2\nhttps://api.example.com/v1\nsk-abcdef123456\n",
			base:   "https://api.example.com/v1",
			key:    "sk-abcdef123456",
			models: []string{"gpt-5.2"},
		},
		{
			name:   "labelled lines in Chinese",
			text:   "模型：deepseek-v4\n网址：https://api.deepseek.com\n密钥：sk-1234567890abc",
			base:   "https://api.deepseek.com",
			key:    "sk-1234567890abc",
			models: []string{"deepseek-v4"},
		},
		{
			name:   "an env block",
			text:   "export OPENAI_BASE_URL=https://api.x.ai/v1\nexport OPENAI_API_KEY=sk-xyz1234567890",
			base:   "https://api.x.ai/v1",
			key:    "sk-xyz1234567890",
			models: nil,
		},
		{
			name:   "a JSON config",
			text:   `{"base_url":"https://api.moonshot.cn/v1","api_key":"sk-1234567890abc","model":"kimi-k2.5","name":"Kimi"}`,
			base:   "https://api.moonshot.cn/v1",
			key:    "sk-1234567890abc",
			models: []string{"kimi-k2.5"},
			pname:  "Kimi",
		},
		{
			name: "a curl example",
			text: "curl https://api.deepseek.com/v1/chat/completions \\\n" +
				"  -H \"Content-Type: application/json\" \\\n" +
				"  -H \"Authorization: Bearer sk-curl1234567890\" \\\n" +
				"  -d '{\"model\":\"deepseek-v4-pro\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'",
			base:   "https://api.deepseek.com/v1",
			key:    "sk-curl1234567890",
			models: []string{"deepseek-v4-pro"},
		},
		{
			name:   "an endpoint pasted whole",
			text:   "网址: https://relay.example.com/api/v1/messages\n模型: claude-sonnet-5\n密钥: sk-ant-abcdefghijklmnop",
			base:   "https://relay.example.com/api/v1",
			key:    "sk-ant-abcdefghijklmnop",
			models: []string{"claude-sonnet-5"},
		},
		{
			name:   "several keys and several models",
			text:   "模型：a-1, b-2\n网址：https://relay.example.com/v1\nkey: sk-one1234567890 sk-two1234567890",
			base:   "https://relay.example.com/v1",
			key:    "sk-one1234567890",
			models: []string{"a-1", "b-2"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := parseAgentPaste(c.text)
			if got.Base != c.base {
				t.Errorf("base %q, want %q", got.Base, c.base)
			}
			if got.key() != c.key {
				t.Errorf("key %q, want %q", got.key(), c.key)
			}
			if !slices.Equal(got.Models, c.models) {
				t.Errorf("models %v, want %v", got.Models, c.models)
			}
			if c.pname != "" && got.Name != c.pname {
				t.Errorf("name %q, want %q", got.Name, c.pname)
			}
		})
	}
}

// A paste adds the provider, fills in its key, fetches its list, tests every
// model and keeps only the ones that answered.
func TestAgentChatAddsTestsAndKeeps(t *testing.T) {
	newAgentHome(t)
	v := newAgentVendor(t, []string{"alpha-1", "beta-2", "gamma-3"}, "beta-2")
	mux := http.NewServeMux()
	providerRoutes(mux, nil)

	events := chat(t, mux, map[string]any{
		"text": "alpha-1\n" + v.URL + "/v1\nsk-test-abcdef123456",
	})
	if ps := problems(events); len(ps) > 0 {
		t.Fatalf("the run refused: %v", ps)
	}
	if got := event(events, "parsed"); got == nil || got.Base != v.URL+"/v1" || got.Key == "" || got.Key == "sk-test-abcdef123456" {
		t.Fatalf("parsed: %+v", got)
	}
	created := event(events, "created")
	if created == nil || created.ID != "custom" {
		t.Fatalf("created: %+v", created)
	}
	if listed := event(events, "listed"); listed == nil || listed.Count != 3 {
		t.Fatalf("listed: %+v", listed)
	}
	// one test line per model, each with what answered
	byModel := map[string]agentEvent{}
	for _, ev := range events {
		if ev.Type == "test" {
			byModel[ev.Model] = ev
		}
	}
	if len(byModel) != 3 {
		t.Fatalf("tested %v", byModel)
	}
	if ev := byModel["beta-2"]; ev.OK == nil || *ev.OK || ev.Status != 404 {
		t.Errorf("beta-2: %+v", ev)
	}
	for _, m := range []string{"alpha-1", "gamma-3"} {
		if ev := byModel[m]; ev.OK == nil || !*ev.OK {
			t.Errorf("%s: %+v", m, ev)
		}
	}
	res := event(events, "result")
	if res == nil || !slices.Equal(res.Kept, []string{"alpha-1", "gamma-3"}) || !slices.Equal(res.Dropped, []string{"beta-2"}) {
		t.Fatalf("result: %+v", res)
	}
	if saved := event(events, "saved"); saved == nil {
		t.Fatal("the run didn't save")
	}
	// the provider is on disk with the key, the URL and only the models that
	// answered: the one that lists a name but can't be called is gone
	p, err := provider.Find("custom")
	if err != nil {
		t.Fatal(err)
	}
	if p.Chat != v.URL+"/v1" || p.Key != "sk-test-abcdef123456" {
		t.Errorf("saved as %q %q", p.Chat, p.Key)
	}
	if !slices.Equal(p.Models, []string{"alpha-1", "gamma-3"}) {
		t.Errorf("picks %v, want the models that answered", p.Models)
	}
	if ps := provider.All(); len(ps) != 1 {
		t.Errorf("added %v", ps)
	}
}

// A paste of a URL that is already added uses that provider: nothing is added
// beside it, and its picks are what the test narrows.
func TestAgentChatNarrowsAnAddedProvider(t *testing.T) {
	newAgentHome(t)
	v := newAgentVendor(t, []string{"alpha-1", "beta-2"}, "")
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-existing123456",
		Chat: v.URL + "/v1", Models: []string{"alpha-1", "beta-2"}}); err != nil {
		t.Fatal(err)
	}
	// beta-2 stops answering from now on
	v.breakModel("beta-2")
	events := chat(t, mux, map[string]any{
		"text": "网址： " + v.URL + "/v1\n模型: alpha-1\nsk-pasted123456789",
	})
	if exist := event(events, "exist"); exist == nil || exist.ID != "relay" {
		t.Fatalf("exist: %+v", exist)
	}
	if created := event(events, "created"); created != nil {
		t.Fatalf("a second provider was added: %+v", created)
	}
	if ps := provider.All(); len(ps) != 1 {
		t.Fatalf("providers: %v", ps)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Models, []string{"alpha-1"}) {
		t.Errorf("picks %v, want only the model that answered", p.Models)
	}
	// the pasted key joined it, the one it had kept
	if !p.HasKey("sk-pasted123456789") || !p.HasKey("sk-existing123456") {
		t.Errorf("keys %v", p.KeyList())
	}
}

// A test that answers nothing leaves the provider as it is, rather than
// saving one with no models at all.
func TestAgentChatAllFailLeavesItAlone(t *testing.T) {
	newAgentHome(t)
	v := newAgentVendor(t, []string{"only-1"}, "")
	v.breakModel("only-1")
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	events := chat(t, mux, map[string]any{"text": "only-1\n" + v.URL + "/v1\nsk-test-abcdef123456"})
	if ps := problems(events); !slices.Contains(ps, "allfail") {
		t.Fatalf("problems %v, want allfail", ps)
	}
	if event(events, "saved") != nil {
		t.Fatal("it saved a provider with no models")
	}
	p, err := provider.Find("custom")
	if err != nil {
		t.Fatal(err) // the provider is there, so the key or the URL can be fixed
	}
	if len(p.Models) != 0 {
		t.Errorf("picks %v, want none written", p.Models)
	}
	if !strings.Contains(strings.Join(problems(events), " "), "allfail") {
		t.Error("allfail wasn't said")
	}
}

// A paste with no key is refused before anything is written.
func TestAgentChatNoKey(t *testing.T) {
	newAgentHome(t)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	events := chat(t, mux, map[string]any{"text": "alpha-1\nhttps://api.example.com/v1\n"})
	if ps := problems(events); !slices.Contains(ps, "nokey") {
		t.Fatalf("problems %v, want nokey", ps)
	}
	if len(provider.All()) != 0 {
		t.Fatalf("added %v", provider.All())
	}
}

// The cap tests the first models and leaves the rest as they were: a model
// nobody asked about is never taken out for not answering.
func TestAgentChatCapKeepsTheUntested(t *testing.T) {
	newAgentHome(t)
	v := newAgentVendor(t, []string{"alpha-1", "beta-2", "gamma-3"}, "beta-2")
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	events := chat(t, mux, map[string]any{
		"text":    v.URL + "/v1\nsk-test-abcdef123456",
		"maxTest": 2,
	})
	if ev := eventOfCode(events, "capped"); ev == nil || ev.Count != 2 || ev.Untested != 1 {
		t.Fatalf("capped: %+v", ev)
	}
	if ev := event(events, "result"); ev == nil || !slices.Equal(ev.Kept, []string{"alpha-1", "gamma-3"}) {
		t.Fatalf("result: %+v", ev)
	}
	p, err := provider.Find("custom")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Models, []string{"alpha-1", "gamma-3"}) {
		t.Errorf("picks %v: the model nobody asked about is kept", p.Models)
	}
}

// A paste of an MCP config adds the servers it names to the library, and
// nothing of a provider is touched (朱, 2026-10-16: the mcpServers block of an
// agent's file, pasted into the window).
func TestAgentChatAddsMCPServers(t *testing.T) {
	newAgentHome(t)
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	// the config the user pasted, as it came
	const memory = `{
  "mcpServers": {
    "codebase-memory-mcp": {
      "command": "E:\\git\\codebase-memory-mcp\\build\\c\\codebase-memory-mcp.exe"
    }
  }
}`
	events := chat(t, mux, map[string]any{"text": memory})
	if ev := event(events, "mcps"); ev == nil || ev.Count != 1 || !slices.Equal(ev.Models, []string{"codebase-memory-mcp"}) {
		t.Fatalf("mcps: %+v", ev)
	}
	if ev := event(events, "created"); ev != nil {
		t.Fatalf("a provider was added by an MCP paste: %+v", ev)
	}
	if ev := event(events, "mcp"); ev == nil || ev.Added != 1 || ev.Updated != 0 || ev.Same != 0 ||
		len(ev.Servers) != 1 || ev.Servers[0].State != "added" || ev.Servers[0].Transport != "stdio" {
		t.Fatalf("mcp: %+v", ev)
	}
	got := library.Servers()
	if len(got) != 1 || got[0].Name != "codebase-memory-mcp" || got[0].Transport != "stdio" ||
		got[0].Command != `E:\git\codebase-memory-mcp\build\c\codebase-memory-mcp.exe` || len(got[0].Agents) != 0 {
		t.Fatalf("library: %+v", got)
	}
	// the same config again: it is already there as it is, and nothing is written
	events = chat(t, mux, map[string]any{"text": memory})
	if ev := event(events, "mcp"); ev == nil || ev.Same != 1 || ev.Added != 0 || ev.Servers[0].State != "same" {
		t.Fatalf("again: %+v", ev)
	}
	// the same name with another command: written over
	events = chat(t, mux, map[string]any{"text": `{"mcpServers": {"codebase-memory-mcp": {"command": "C:\\other\\cbm.exe", "args": ["--stdio"]}}}`})
	if ev := event(events, "mcp"); ev == nil || ev.Updated != 1 || ev.Servers[0].State != "updated" {
		t.Fatalf("updated: %+v", ev)
	}
	if got := library.Servers(); got[0].Command != `C:\other\cbm.exe` || !slices.Equal(got[0].Args, []string{"--stdio"}) {
		t.Fatalf("library after the change: %+v", got)
	}
	// several at once, a url one among them, with its env and headers
	events = chat(t, mux, map[string]any{"text": `{"mcpServers": {"docs": {"type": "http", "url": "https://mcp.example.com/mcp", "headers": {"Authorization": "Bearer x"}}, "fs": {"command": "npx", "args": ["-y", "fs-mcp"], "env": {"KEY": "v"}}}}`})
	byName := map[string]library.Server{}
	for _, s := range library.Servers() {
		byName[s.Name] = s
	}
	if ev := event(events, "mcp"); ev == nil || ev.Added != 2 || ev.Count != 2 {
		t.Fatalf("several: %+v", ev)
	}
	if s := byName["docs"]; s.Transport != "http" || s.URL != "https://mcp.example.com/mcp" || s.Headers["Authorization"] != "Bearer x" {
		t.Fatalf("docs: %+v", s)
	}
	if s := byName["fs"]; s.Command != "npx" || !slices.Equal(s.Args, []string{"-y", "fs-mcp"}) || s.Env["KEY"] != "v" {
		t.Fatalf("fs: %+v", s)
	}
	// an entry that can't be a server says why, and the rest still go in
	events = chat(t, mux, map[string]any{"text": `{"mcpServers": {"broken": {"url": "not-a-url"}, "fine": {"command": "fine-mcp"}}}`})
	if ev := event(events, "mcp"); ev == nil || ev.Count != 1 || ev.Servers[0].Name != "fine" {
		t.Fatalf("one broken: %+v", ev)
	}
	// a provider paste is still a provider paste
	events = chat(t, mux, map[string]any{"text": "alpha-1\nhttp://127.0.0.1:9/v1\nsk-abcdef123456"})
	if ev := event(events, "mcps"); ev != nil {
		t.Fatalf("a provider paste read as an MCP config: %+v", ev)
	}
	if ev := event(events, "parsed"); ev == nil || ev.Base != "http://127.0.0.1:9/v1" {
		t.Fatalf("parsed: %+v", ev)
	}
}
