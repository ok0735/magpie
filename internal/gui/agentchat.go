package gui

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// The chat window beside the Providers page: paste a model name, a base URL
// and a key into it and magpie does the rest — finds the provider that URL
// names (or adds one), fills in the key and the models, asks the vendor for
// its model list, sends each model the smallest request a test sends, and
// keeps only the ones that answered: a model that answers is served, one that
// lists a name but can't be called is taken out of the picks. The run is
// streamed back as one JSON object per line, so the window shows every step
// while it happens.

// agentChatRoutes serves the chat window (POST /api/agent/chat).
func agentChatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/chat", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
			// Name names the provider to add, when the paste has none
			Name string `json:"name"`
			// Provider works on one already added: the paste completes its
			// key and its models, and no provider is looked for
			Provider string `json:"provider"`
			// MaxTest is how many models are tested at most; 0 is all of them
			MaxTest int `json:"maxTest"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		st := newAgentStream(rw)
		runAgentChat(r.Context(), st, in.Text, in.Name, in.Provider, in.MaxTest)
		st.send(agentEvent{Type: "done"})
	})
}

// agentEvent is one line of the run: its Type says which fields matter.
//
//	say      text, level          a vendor's own words, shown as they are
//	parsed   base, key, models…   what the paste was read as
//	exist    id, name             a provider at that host is already added
//	keyadded id, added            its keys took the pasted key
//	preset   id, name             the URL is a preset's
//	creating base                 the provider is being added
//	created  id, name, speaks     the provider was added
//	listed   count, dropped       the vendor's list came back
//	testing  count                how many models are being tested
//	test     model, ok, status…   one model's answer
//	result   kept, dropped…       what the test kept and what it took out
//	saved    id, kept             the provider was saved with those models
//	problem  code, detail         the run stopped, or went on with a warning
//	done                          the run is over
type agentEvent struct {
	Type   string `json:"type"`
	Level  string `json:"level,omitempty"`
	Text   string `json:"text,omitempty"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`

	Base   string   `json:"base,omitempty"`
	Host   string   `json:"host,omitempty"`
	Key    string   `json:"key,omitempty"`
	Keys   int      `json:"keys,omitempty"`
	Models []string `json:"models,omitempty"`
	Name   string   `json:"name,omitempty"`
	Preset string   `json:"preset,omitempty"`

	ID     string   `json:"id,omitempty"`
	Added  int      `json:"added,omitempty"`
	Speaks []string `json:"speaks,omitempty"`

	Count    int      `json:"count,omitempty"`
	Tested   int      `json:"tested,omitempty"`
	Untested int      `json:"untested,omitempty"`
	Dropped  []string `json:"dropped,omitempty"`
	Kept     []string `json:"kept,omitempty"`

	Model    string `json:"model,omitempty"`
	OK       *bool  `json:"ok,omitempty"`
	Status   int    `json:"status,omitempty"`
	Millis   int64  `json:"ms,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Error    string `json:"error,omitempty"`

	// Servers, Added, Updated and Same are an MCP config's: what each server
	// of it is, and how many of them the library took (its Added), wrote
	// over, and already had as they are
	Servers []agentMCP `json:"servers,omitempty"`
	Updated int        `json:"updated,omitempty"`
	Same    int        `json:"same,omitempty"`
}

// agentMCP is one server of a pasted MCP config, as the window shows it.
type agentMCP struct {
	Name      string `json:"name"`
	Transport string `json:"transport,omitempty"`
	Target    string `json:"target,omitempty"`
	// State is what the library did with it: added, updated, or same for one
	// it already had as the paste has it
	State string `json:"state,omitempty"`
}

// agentStream writes the run's events as they happen, one JSON object per
// line: a window reading them one by one shows a long test while it runs.
type agentStream struct {
	enc  *json.Encoder
	fl   http.Flusher
	dead bool
}

func newAgentStream(rw http.ResponseWriter) *agentStream {
	h := rw.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // a proxy before magpie web: send each line on
	rw.WriteHeader(http.StatusOK)
	s := &agentStream{enc: json.NewEncoder(rw)}
	if f, ok := rw.(http.Flusher); ok {
		s.fl = f
	}
	return s
}

func (s *agentStream) send(ev agentEvent) {
	if s.dead {
		return
	}
	if err := s.enc.Encode(ev); err != nil {
		// the window was closed or the page reloaded: the run goes on
		// quietly and what it did is still saved
		s.dead = true
		return
	}
	if s.fl != nil {
		s.fl.Flush()
	}
}

// agentRun works one paste through to a saved provider.
type agentRun struct {
	st *agentStream
	// MaxTest is how many models are tested at most, 0 for all of them
	MaxTest int
	// KeepProvider turns off the search for a provider already added at the
	// paste's host, so the run adds beside it (the tests use it)
	KeepProvider bool
}

// agentTestChunk is how many models are tested between two lines of the run:
// a chunk's answers are all written before the next chunk is asked, so a long
// test reads as it goes.
const agentTestChunk = 8

// The run's own bounds: what a vendor is given to answer with, and how long
// its models are given together.
const (
	agentDetectWait = 60 * time.Second
	agentListWait   = 60 * time.Second
	agentTestWait   = 30 * time.Minute
)

// runAgentChat is the whole run: read the paste, find or add the provider,
// fetch its list, test the models, keep the ones that answered and save.
func runAgentChat(ctx context.Context, st *agentStream, text, name, pick string, maxTest int) {
	r := &agentRun{st: st, MaxTest: maxTest}
	// an MCP config pasted whole is a run of its own: the servers it names are
	// added to the library, and nothing of a provider is touched (朱,
	// 2026-10-16: the mcpServers block of an agent's file, pasted into the
	// window)
	if servers := mcpsOfText(text); len(servers) > 0 {
		names := make([]string, 0, len(servers))
		for _, s := range servers {
			names = append(names, s.Name)
		}
		st.send(agentEvent{Type: "mcps", Count: len(servers), Models: names})
		r.addMCP(servers)
		return
	}
	paste := parseAgentPaste(text)
	key := paste.key()
	st.send(agentEvent{Type: "parsed", Base: paste.Base, Host: provider.HostOf(paste.Base), Key: provider.Mask(key),
		Keys: len(paste.Keys), Models: paste.Models, Name: paste.Name})
	switch {
	case paste.Base == "" && key == "" && pick == "":
		st.send(agentEvent{Type: "problem", Code: "noparse"})
		return
	case paste.Base == "" && pick == "":
		st.send(agentEvent{Type: "problem", Code: "nobase"})
		return
	case key == "" && pick == "":
		st.send(agentEvent{Type: "problem", Code: "nokey"})
		return
	}
	id, err := r.provider(ctx, paste, name, pick, key)
	if err != nil {
		st.send(agentEvent{Type: "problem", Code: "noprovider", Detail: err.Error()})
		return
	}
	if id == "" {
		return
	}
	r.work(ctx, id, paste)
}

// provider finds the provider the paste names, or adds it: one already added
// at that host is used as it is; else a preset the URL names is what it is
// added from, and a URL no preset names is asked which of magpie's APIs it
// answers on.
func (r *agentRun) provider(ctx context.Context, paste agentPaste, name, pick, key string) (string, error) {
	if pick != "" {
		p, err := provider.Find(pick)
		if err != nil {
			return "", err
		}
		return r.takeKey(*p, paste)
	}
	host := provider.HostOf(paste.Base)
	if !r.KeepProvider && host != "" {
		if p := agentProviderAt(host, paste.Base); p != nil {
			return r.takeKey(*p, paste)
		}
	}
	label := pickName(name, paste.Name)
	preset, hasPreset := agentPresetFor(paste.Base)
	var add provider.Provider
	if hasPreset {
		p, err := provider.FromPreset(preset.ID)
		if err != nil {
			return "", err
		}
		add = p
		if label == "" {
			label = p.Name
		}
		r.st.send(agentEvent{Type: "preset", ID: preset.ID, Name: label})
	} else {
		// the API the base URL is for, found by asking: the smallest request
		// of each of magpie's, as the editor's Detect asks it
		base := provider.DetectBase(paste.Base, provider.Chat)
		add = provider.Provider{Name: label, Chat: base, BaseAPI: string(provider.Chat)}
		speaks, why := agentSpeak(ctx, add, paste)
		if len(speaks) == 0 {
			// nothing answered: the OpenAI-compatible URL is kept and the
			// model test says the rest
			r.st.send(agentEvent{Type: "problem", Level: "warn", Code: "noapi", Detail: why})
		}
		for _, d := range speaks {
			switch d.Protocol {
			case provider.Chat:
				add.Chat = d.Base
			case provider.Responses:
				add.Responses = d.Base
			case provider.Anthropic:
				add.Anthropic = d.Base
			case provider.Gemini:
				add.Gemini = d.Base
			}
		}
	}
	if add.Name == "" {
		add.Name = label
	}
	add.Key = key
	// no picks yet: they are what the test answers (an empty list serves the
	// vendor's own, which the test then narrows)
	add.Models = nil
	r.st.send(agentEvent{Type: "creating", Base: add.Chat})
	id, err := provider.Add(add)
	if err != nil {
		return "", err
	}
	p, err := provider.Find(id)
	if err != nil {
		return id, err
	}
	speaks := make([]string, 0, 4)
	for _, sp := range p.Speaks() {
		speaks = append(speaks, string(sp))
	}
	r.st.send(agentEvent{Type: "created", ID: id, Name: p.Name, Speaks: speaks})
	if len(paste.Keys) > 1 {
		// the rest of the paste's keys become keys of their own, as the
		// editor's Paste several does
		if added, _, err := provider.AddKeys(id, paste.Keys[1:], ""); err == nil && added > 0 {
			r.st.send(agentEvent{Type: "keyadded", ID: id, Added: added})
		}
	}
	return id, nil
}

// takeKey uses a provider already added: its host is the paste's, so it is
// the one meant, and the pasted key joins its keys when it isn't one of them.
func (r *agentRun) takeKey(p provider.Provider, paste agentPaste) (string, error) {
	r.st.send(agentEvent{Type: "exist", ID: p.ID, Name: p.Name})
	if len(paste.Keys) == 0 || p.HasKey(paste.Keys[0]) {
		return p.ID, nil
	}
	added, had, err := provider.AddKeys(p.ID, paste.Keys, "")
	switch {
	case err != nil && had == 0:
		r.st.send(agentEvent{Type: "say", Level: "warn", Text: err.Error()})
	case added > 0:
		r.st.send(agentEvent{Type: "keyadded", ID: p.ID, Added: added})
	}
	return p.ID, nil
}

// agentProviderAt is a provider already added at that host, nil when none is:
// the paste's URL is one of its own.
func agentProviderAt(host, base string) *provider.Provider {
	if host == "" {
		return nil
	}
	base = cleanAgentBase(base)
	for _, p := range provider.All() {
		if p.Account != nil || p.ID == "" {
			continue
		}
		for _, u := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
			if u == "" {
				continue
			}
			if provider.HostOf(u) == host || cleanAgentBase(u) == base {
				q := p
				return &q
			}
		}
	}
	return nil
}

// agentPresetFor is the preset a URL names, by its host and then by the most
// of the path it spells. The two presets asked with something other than a
// key (Vertex AI's credentials, Azure's own endpoint) are left out: a pasted
// key is not what they are added with.
func agentPresetFor(base string) (provider.PresetDef, bool) {
	host := provider.HostOf(base)
	if host == "" {
		return provider.PresetDef{}, false
	}
	var best provider.PresetDef
	bestLen := -1
	for _, pr := range provider.Presets() {
		if pr.ID == provider.VertexPreset || pr.ID == provider.AzurePreset {
			continue
		}
		bases := []string{pr.Chat, pr.Responses, pr.Anthropic}
		for _, reg := range pr.Regions {
			bases = append(bases, reg.Chat, reg.Responses, reg.Anthropic)
		}
		for _, b := range bases {
			if b == "" || provider.HostOf(b) != host {
				continue
			}
			if n := len(strings.TrimRight(b, "/")); n > bestLen {
				best, bestLen = pr, n
			}
		}
	}
	return best, bestLen >= 0
}

// agentSpeak asks which of magpie's APIs the vendor answers on, with the same
// smallest requests the editor's Detect sends. It answers what answered, and
// why when nothing did.
func agentSpeak(ctx context.Context, ap provider.Provider, paste agentPaste) ([]provider.Detection, string) {
	ctx, cancel := context.WithTimeout(ctx, agentDetectWait)
	defer cancel()
	model := ""
	if len(paste.Models) > 0 {
		model = paste.Models[0]
	}
	got, err := ap.Detect(ctx, cleanAgentBase(paste.Base), model, paste.Models...)
	if err != nil {
		return nil, err.Error()
	}
	var ok []provider.Detection
	var why []string
	for _, d := range got {
		if d.OK {
			ok = append(ok, d)
			continue
		}
		if d.Error != "" {
			why = append(why, string(d.Protocol)+": "+d.Error)
		}
	}
	if len(ok) > 0 {
		return ok, ""
	}
	return nil, strings.Join(why, " · ")
}

// work fetches the provider's list, tests its models and saves the ones that
// answered.
func (r *agentRun) work(ctx context.Context, id string, paste agentPaste) {
	p, err := provider.Find(id)
	if err != nil {
		r.st.send(agentEvent{Type: "problem", Code: "noprovider", Detail: err.Error()})
		return
	}
	if why := p.ModelTest(); why != "" {
		// a decision API, or an agent's own API magpie translates: there is
		// no endpoint to send one model's request to
		r.st.send(agentEvent{Type: "problem", Code: "notestable", Detail: why})
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, agentListWait)
	ms, dropped, err := p.Refetch(listCtx)
	cancel()
	if err != nil {
		r.st.send(agentEvent{Type: "problem", Code: "nolist", Detail: err.Error()})
		return
	}
	now, err := provider.Find(id)
	if err != nil {
		r.st.send(agentEvent{Type: "problem", Code: "noprovider", Detail: err.Error()})
		return
	}
	listed := make([]string, 0, len(ms))
	for _, m := range now.Available() {
		listed = append(listed, m.ID)
	}
	if len(listed) == 0 {
		for _, m := range ms {
			listed = append(listed, m.ID)
		}
	}
	r.st.send(agentEvent{Type: "listed", ID: id, Count: len(listed), Dropped: dropped})
	if len(listed) == 0 {
		r.st.send(agentEvent{Type: "problem", Code: "nomodels"})
		return
	}

	// what is tested: what the paste named, then the picks the provider has
	// (the rest of the vendor's list isn't served anyway), else everything it
	// lists
	set := agentTestSet(*now, listed, paste.Models)
	if len(set) == 0 {
		r.st.send(agentEvent{Type: "problem", Code: "nomodels"})
		return
	}
	untested := []string{}
	capped := false
	if r.MaxTest > 0 && len(set) > r.MaxTest {
		untested, set, capped = set[r.MaxTest:], set[:r.MaxTest], true
	}
	if capped {
		r.st.send(agentEvent{Type: "problem", Level: "warn", Code: "capped", Count: len(set), Untested: len(untested)})
	}
	r.st.send(agentEvent{Type: "testing", Count: len(set)})

	testCtx, cancelTest := context.WithTimeout(ctx, agentTestWait)
	defer cancelTest()
	answer := map[string]provider.Result{}
	done := 0
	for start := 0; start < len(set); start += agentTestChunk {
		if testCtx.Err() != nil {
			untested = append(untested, set[start:]...)
			set = set[:start]
			r.st.send(agentEvent{Type: "problem", Level: "warn", Code: "timeout"})
			break
		}
		chunk := set[start:min(start+agentTestChunk, len(set))]
		got := now.TestModels(testCtx, chunk)
		for i, m := range chunk {
			if i >= len(got) {
				break
			}
			res := got[i]
			res.Model = m
			answer[m] = res
			ok := res.OK
			r.st.send(agentEvent{Type: "test", Model: m, OK: &ok, Status: res.Status,
				Millis: res.Millis, Protocol: string(res.Protocol), Error: res.Error})
		}
		done += len(chunk)
	}

	var kept, failed []string
	for _, m := range set {
		if answer[m].OK {
			kept = append(kept, m)
		} else {
			failed = append(failed, m)
		}
	}
	kept = append(kept, untested...)
	r.st.send(agentEvent{Type: "result", Kept: kept, Dropped: failed, Tested: done, Untested: len(untested)})

	if len(kept) == 0 {
		// nothing answered: the provider is left as it is, so a key or a URL
		// that isn't right can be fixed rather than an empty provider kept
		r.st.send(agentEvent{Type: "problem", Code: "allfail", Count: len(failed)})
		return
	}
	if err := agentSavePicks(now, kept); err != nil {
		r.st.send(agentEvent{Type: "problem", Code: "nosave", Detail: err.Error()})
		return
	}
	r.st.send(agentEvent{Type: "saved", ID: id, Kept: kept, Count: len(kept), Dropped: failed})
}

// agentTestSet is the models a run tests: the paste's first, then the picks
// the provider has, else everything the vendor lists. A model the user
// disabled is left out of the test — and so kept, never dropped.
func agentTestSet(p provider.Provider, listed, pasted []string) []string {
	pool := listed
	if len(p.Models) > 0 {
		pool = p.Models
	}
	var set []string
	for _, m := range append(append([]string{}, pasted...), pool...) {
		if m == "" || slices.Contains(set, m) {
			continue
		}
		if _, off := provider.ModelDisabled(p.ID, m); off {
			continue
		}
		set = append(set, m)
	}
	return set
}

// agentSavePicks saves the provider with those models as its picks: the ones
// that answered are served, the ones that didn't are not.
func agentSavePicks(p *provider.Provider, kept []string) error {
	p.Models = slices.DeleteFunc(slices.Clone(kept), func(m string) bool { return m == "" })
	p.NoModels = false
	return provider.Save(*p)
}

// pickName is the name a provider is added under: the one given beside the
// paste, else the one in it.
func pickName(name, pasted string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	pasted = strings.TrimSpace(pasted)
	if pasted == "" || strings.Contains(pasted, "://") {
		return ""
	}
	return pasted
}

// ---------------------------------------------------------------------------
// MCP configs pasted whole
// ---------------------------------------------------------------------------

// mcpsOfText reads a paste that is an MCP config: the mcpServers block an
// agent's file holds (or mcp-servers, mcp, servers), or one server's own
// object with a command or a url. A paste that isn't one answers none, and
// the run goes on as a provider's.
func mcpsOfText(text string) []library.Server {
	body := strings.TrimSpace(text)
	if body == "" {
		return nil
	}
	// the JSON object in it: a fenced block, or a line of prose around it
	i, j := strings.Index(body, "{"), strings.LastIndex(body, "}")
	if i < 0 || j <= i {
		return nil
	}
	body = body[i : j+1]
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &doc) != nil {
		// a config written as an agent's own file is, comments and all
		if json.Unmarshal(jsonc.ToJSON([]byte(body)), &doc) != nil {
			return nil
		}
	}
	var raw map[string]json.RawMessage
	for _, key := range []string{"mcpServers", "mcp-servers", "mcp", "servers"} {
		raw = nil
		if r, ok := doc[key]; ok && json.Unmarshal(r, &raw) == nil && len(raw) > 0 {
			break
		}
	}
	var out []library.Server
	if len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			if s, err := mcpServer(name, raw[name]); err == nil {
				out = append(out, s)
			}
		}
		return out
	}
	// one server's own object: it needs a command or a url to be one
	if _, cmd := doc["command"]; !cmd {
		if _, url := doc["url"]; !url {
			return nil
		}
	}
	var named struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(body), &named)
	name := cmp.Or(strings.TrimSpace(named.Name), "mcp")
	if s, err := mcpServer(name, []byte(body)); err == nil {
		out = append(out, s)
	}
	return out
}

// mcpServer is one entry of an MCP config as the library keeps a server: a
// command at "command" (with "args" and "env"), or a url (with "headers"),
// http unless it says sse.
func mcpServer(name string, entry json.RawMessage) (library.Server, error) {
	var j struct {
		Command   string            `json:"command"`
		Args      []string          `json:"args"`
		Env       map[string]string `json:"env"`
		URL       string            `json:"url"`
		Type      string            `json:"type"`
		Transport string            `json:"transport"`
		Headers   map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(entry, &j); err != nil {
		return library.Server{}, err
	}
	s := library.Server{Name: strings.TrimSpace(name), Args: j.Args, Env: j.Env, Headers: j.Headers, Agents: []string{}}
	if cmd := strings.TrimSpace(j.Command); cmd != "" {
		s.Transport, s.Command = "stdio", cmd
		s.URL, s.Headers = "", nil
		return s, nil
	}
	s.Transport = "http"
	switch strings.ToLower(strings.TrimSpace(cmp.Or(j.Transport, j.Type))) {
	case "sse":
		s.Transport = "sse"
	case "stdio":
		// a stdio entry with no command: the library says why it can't be one
		s.Transport = "stdio"
	case "http", "streamable", "streamable-http":
		s.Transport = "http"
	}
	s.URL = strings.TrimSpace(j.URL)
	s.Command, s.Args, s.Env = "", nil, nil
	return s, nil
}

// sameMCP says whether the library's server is the one the paste names, so a
// paste of what is already there writes nothing.
func sameMCP(a, b library.Server) bool {
	return a.Transport == b.Transport && a.Command == b.Command && a.URL == b.URL &&
		slices.Equal(a.Args, b.Args) && maps.Equal(orEmpty(a.Env), orEmpty(b.Env)) && maps.Equal(orEmpty(a.Headers), orEmpty(b.Headers))
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// mcpTarget is what a server is reached by, for the window to show.
func mcpTarget(s library.Server) string {
	if s.Command != "" {
		return strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
	}
	return s.URL
}

// addMCP adds the servers of a pasted MCP config to the library: one it
// already has as the paste has it is left alone, one whose settings changed
// is written over, and one that is new is added. Nothing is given to an
// agent — the Library's own tick row does that — so a paste can't change what
// an agent starts.
func (r *agentRun) addMCP(servers []library.Server) {
	have := map[string]library.Server{}
	for _, s := range library.Servers() {
		have[s.Name] = s
	}
	var rows []agentMCP
	added, updated, same := 0, 0, 0
	for _, s := range servers {
		state := "added"
		old, known := have[s.Name]
		switch {
		case known && sameMCP(old, s):
			state = "same"
		case known:
			state = "updated"
		}
		if state != "same" {
			// an empty old is a new server; one there by that name takes the
			// paste's settings over its own
			old := ""
			if known {
				old = strings.TrimSpace(s.Name)
			}
			if _, err := library.SaveServer(old, s); err != nil {
				r.st.send(agentEvent{Type: "say", Level: "warn", Text: s.Name + ": " + err.Error()})
				continue
			}
		}
		switch state {
		case "added":
			added++
		case "updated":
			updated++
		default:
			same++
		}
		rows = append(rows, agentMCP{Name: s.Name, Transport: s.Transport, Target: mcpTarget(s), State: state})
	}
	r.st.send(agentEvent{Type: "mcp", Servers: rows, Count: len(rows), Added: added, Updated: updated, Same: same})
}

// ---------------------------------------------------------------------------
// reading the paste
// ---------------------------------------------------------------------------

// agentPaste is a paste read into the parts a provider is made of.
type agentPaste struct {
	Base   string   // the base URL, with the endpoint it named cut off
	Keys   []string // the keys, in the order they were pasted
	Models []string // the models named, if any
	Name   string   // a name given for the provider, if any
}

func (p agentPaste) key() string {
	if len(p.Keys) > 0 {
		return p.Keys[0]
	}
	return ""
}

// agentLabel is one line of a paste — "Key: sk-…", "base_url = …", "网址：…"
// — with the name it goes by, and the value after it.
var agentLabel = regexp.MustCompile(`(?i)^["']?(` +
	`api[ _-]?key|apikey|key|token|secret|` +
	`base[ _-]?url|baseurl|openai[ _-]?base[ _-]?url|anthropic[ _-]?base[ _-]?url|` +
	`url|endpoint|host|address|` +
	`model|models|model[ _-]?name|default[ _-]?model|` +
	`name|provider|provider[ _-]?name|` +
	`密钥|秘钥|密码|令牌|钥匙|` +
	`网址|地址|接口地址|服务器地址|` +
	`模型|模型名|模型名称|` +
	`名称|名字|供应商|供应商名称` +
	`)["']?\s*[:=：]\s*(.+)$`)

// What a paste's lines are read with: a URL, the prefixed keys every vendor
// hands out, a bare token long enough to be nobody's model name, a model's id
// and the words a shell line leads with.
var (
	agentURLRe     = regexp.MustCompile(`(?i)https?://[^\s"'<>()\[\]{},;]+`)
	agentKeyRe     = regexp.MustCompile(`\b(?:sk|gsk|hf|xai|nvapi|pplx|anthropic)[-_.][A-Za-z0-9._\-]{8,}\b`)
	agentBareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_+=/]{32,}$`)
	agentModelRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,79}$`)
	agentDigitRe   = regexp.MustCompile(`[0-9]`)
	agentFillerRe  = regexp.MustCompile(`(?i)^(?:export|set|const|let|var|\$env:?)\b`)
)

// agentEndpointSuffixes are the paths a paste names instead of a base: one
// pasted whole is cut off it, as the editor's respellURL does. The /v1 of an
// Anthropic Messages URL stays: it is the OpenAI-style base beside it, and
// provider.DetectBase takes it off for Anthropic's own.
var agentEndpointSuffixes = []string{
	"/chat/completions", "/completions", "/responses", "/messages", "/models",
}

// parseAgentPaste reads a paste — a key and a URL on their own lines, an
// editor's form, a JSON config, a curl example, an env block — into the parts
// a provider is made of.
func parseAgentPaste(text string) agentPaste {
	out := agentPaste{}
	text = strings.TrimSpace(text)
	if text == "" {
		return out
	}
	// a JSON object pasted whole (an API config, a request body)
	if strings.HasPrefix(text, "{") {
		out = agentFromJSON(text, out)
	}
	// a curl example: its URL, its auth header and the model in its body
	if strings.Contains(strings.ToLower(text), "curl") {
		out = agentFromCurl(text, out)
	}
	seenURL := map[string]bool{}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if kind, value, ok := agentLabelLine(line); ok {
			switch kind {
			case "key":
				out.Keys = append(out.Keys, provider.SplitKeys(value)...)
			case "base":
				if u := agentURLRe.FindString(value); u != "" && out.Base == "" {
					out.Base = cleanAgentBase(u)
				}
			case "model":
				out.Models = append(out.Models, agentModels(value)...)
			case "name":
				if out.Name == "" {
					out.Name = agentTrim(value)
				}
			}
			continue
		}
		// a bare URL
		if u := agentURLRe.FindString(line); u != "" {
			if !seenURL[u] {
				seenURL[u] = true
				if out.Base == "" {
					out.Base = cleanAgentBase(u)
				}
			}
			continue
		}
		// a bare key: one with a vendor's prefix, or a long token with none of
		// a model id's dashes and dots
		if k := agentKeyRe.FindString(line); k != "" && agentRealKey(k) {
			out.Keys = append(out.Keys, k)
			continue
		}
		if agentBareKeyRe.MatchString(line) {
			out.Keys = append(out.Keys, line)
			continue
		}
		// a bare model name, or a list of them
		out.Models = append(out.Models, agentModels(line)...)
	}
	if out.Base == "" {
		if u := agentURLRe.FindString(text); u != "" {
			out.Base = cleanAgentBase(u)
		}
	}
	if len(out.Keys) == 0 {
		if k := agentKeyRe.FindString(text); k != "" {
			out.Keys = append(out.Keys, k)
		}
	}
	out.Keys = slices.DeleteFunc(dedupe(out.Keys), func(k string) bool { return !agentRealKey(k) })
	out.Models = dedupe(out.Models)
	return out
}

// agentLabelLine reads a "label: value" line; kind is "key", "base", "model"
// or "name".
func agentLabelLine(line string) (kind, value string, ok bool) {
	m := agentLabel.FindStringSubmatch(agentTrimPrefix(line))
	if m == nil {
		return "", "", false
	}
	name := strings.ToLower(m[1])
	switch {
	case strings.Contains(name, "密钥"), strings.Contains(name, "秘钥"), strings.Contains(name, "密码"),
		strings.Contains(name, "令牌"), strings.Contains(name, "钥匙"), strings.Contains(name, "key"),
		strings.Contains(name, "token"), strings.Contains(name, "secret"):
		return "key", agentTrim(m[2]), true
	case strings.Contains(name, "网址"), strings.Contains(name, "地址"), strings.Contains(name, "url"),
		strings.Contains(name, "endpoint"), strings.Contains(name, "host"):
		return "base", agentTrim(m[2]), true
	case strings.Contains(name, "模型"), strings.Contains(name, "model"):
		return "model", agentTrim(m[2]), true
	case strings.Contains(name, "名称"), strings.Contains(name, "名字"), strings.Contains(name, "供应商"),
		name == "name", strings.HasPrefix(name, "provider"):
		return "name", agentTrim(m[2]), true
	}
	return "", "", false
}

// agentTrimPrefix takes off what a shell line carries before its label:
// "export ", "set ", "$env:" and the like.
func agentTrimPrefix(line string) string {
	if m := agentFillerRe.FindString(line); m != "" {
		line = strings.TrimSpace(line[len(m):])
	}
	return line
}

// agentTrim takes the quotes, the trailing comma and the whitespace off a
// pasted value.
func agentTrim(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ",")
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		for _, q := range []string{`"`, `'`, "`"} {
			if strings.HasPrefix(s, q) && strings.HasSuffix(s, q) {
				s = strings.TrimSpace(s[1 : len(s)-1])
				break
			}
		}
	}
	return s
}

// agentModels reads the models out of a value: commas, spaces and the Chinese
// separators alike.
func agentModels(v string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == '，' || r == '、' || r == ';' || r == '；' || r == '|' || r == ' ' || r == '\t'
	}) {
		if f = agentTrim(f); f != "" && agentLooksModel(f) {
			out = append(out, f)
		}
	}
	return out
}

// agentLooksModel says whether a bare word could be a model's id: a dash, a
// dot, a slash or a digit in it, so "hello" is left alone.
func agentLooksModel(s string) bool {
	if !agentModelRe.MatchString(s) {
		return false
	}
	return strings.ContainsAny(s, "-./:") || agentDigitRe.MatchString(s)
}

// agentRealKey says whether a token is a key rather than a model's name: a
// vendor's prefix, a JWT, or a long token with none of a model id's dashes
// and dots.
func agentRealKey(k string) bool {
	if agentKeyRe.MatchString(k) || strings.HasPrefix(strings.ToLower(k), "eyj") {
		return true
	}
	return agentBareKeyRe.MatchString(k)
}

// cleanAgentBase is a pasted URL as a provider keeps it: the endpoint it named
// cut off, and no trailing slash.
func cleanAgentBase(u string) string {
	u = agentTrim(u)
	if i := strings.IndexAny(u, "#?"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimRight(u, "/")
	for _, end := range agentEndpointSuffixes {
		if strings.HasSuffix(strings.ToLower(u), end) {
			u = strings.TrimRight(u[:len(u)-len(end)], "/")
			break
		}
	}
	return u
}

// agentFromJSON reads a JSON object pasted whole: its key, its base URL, its
// models and its name, under the names an API config, an import link and an
// OpenAI example use.
func agentFromJSON(text string, out agentPaste) agentPaste {
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return out
	}
	flat := map[string]string{}
	agentFlatten("", v, flat)
	get := func(names ...string) string {
		for _, n := range names {
			for k, val := range flat {
				if strings.EqualFold(strings.Trim(k, "_"), strings.Trim(n, "_")) {
					return val
				}
			}
		}
		return ""
	}
	if out.Base == "" {
		if u := get("base_url", "openai_base_url", "anthropic_base_url", "url", "endpoint", "host", "api_base", "api_url"); strings.Contains(u, "://") {
			out.Base = cleanAgentBase(u)
		}
	}
	if len(out.Keys) == 0 {
		if k := get("api_key", "apikey", "key", "token", "auth_token", "access_token", "openai_api_key", "anthropic_api_key"); k != "" && agentRealKey(k) {
			out.Keys = append(out.Keys, k)
		}
	}
	if len(out.Models) == 0 {
		if m := get("model", "models", "default_model", "model_name"); m != "" {
			out.Models = append(out.Models, agentModels(m)...)
		}
	}
	if out.Name == "" {
		out.Name = strings.TrimSpace(get("name", "provider", "provider_name"))
	}
	return out
}

// agentFlatten walks a JSON object into dotted, lower-case keys, so a nested
// config ("api": {"key": …}) reads as "api_key" too.
func agentFlatten(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			agentFlatten(prefix+k+"_", val, out)
		}
	case string:
		out[strings.TrimSuffix(prefix, "_")] = x
	case []any:
		var parts []string
		for _, item := range x {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			out[strings.TrimSuffix(prefix, "_")] = strings.Join(parts, ",")
		}
	}
}

// agentFromCurl reads a curl example: the URL it asks, the key in its
// Authorization, x-api-key or api-key header, and the model in its body.
func agentFromCurl(text string, out agentPaste) agentPaste {
	blob := joinAgentContinuations(text)
	if u := agentCurlURL(blob); u != "" && out.Base == "" {
		out.Base = cleanAgentBase(u)
	}
	if len(out.Keys) == 0 {
		for _, h := range agentCurlHeaders(blob) {
			name, value, ok := strings.Cut(h, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "authorization":
				if _, tok, ok := strings.Cut(strings.TrimSpace(value), " "); ok {
					if k := agentTrim(tok); agentRealKey(k) {
						out.Keys = append(out.Keys, k)
					}
				}
			case "x-api-key", "api-key", "x-goog-api-key":
				if k := agentTrim(value); agentRealKey(k) {
					out.Keys = append(out.Keys, k)
				}
			}
		}
	}
	if len(out.Models) == 0 {
		for _, body := range agentCurlBodies(blob) {
			var v struct {
				Model string `json:"model"`
			}
			if json.Unmarshal([]byte(body), &v) != nil {
				continue
			}
			out.Models = append(out.Models, agentModels(v.Model)...)
		}
	}
	return out
}

// joinAgentContinuations joins a command's lines the way a shell would: a
// line ending in \ (cmd's ^, PowerShell's backtick) goes on.
func joinAgentContinuations(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out strings.Builder
	for i, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if strings.HasSuffix(line, "\\") || strings.HasSuffix(line, "^") || strings.HasSuffix(line, "`") {
			out.WriteString(strings.TrimRight(line[:len(line)-1], " \t"))
			out.WriteString(" ")
			continue
		}
		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// agentCurlURL is the URL a curl command asks: the first http(s) one, which is
// what --url and a bare argument both give.
func agentCurlURL(blob string) string {
	for _, f := range agentShellFields(blob) {
		if strings.Contains(f, "://") {
			if u := agentURLRe.FindString(f); u != "" {
				return u
			}
		}
	}
	return agentURLRe.FindString(blob)
}

// agentCurlHeaders are the -H and --header values of a curl command.
func agentCurlHeaders(blob string) []string {
	var out []string
	fields := agentShellFields(blob)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "-H" || fields[i] == "--header" {
			if i+1 < len(fields) {
				out = append(out, fields[i+1])
			}
			i++
		}
	}
	return out
}

// agentCurlBodies are the -d and --data values of a curl command, whatever
// escapes they were written with.
func agentCurlBodies(blob string) []string {
	var out []string
	fields := agentShellFields(blob)
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "-d", "--data", "--data-raw", "--data-binary", "--data-ascii":
			if i+1 < len(fields) {
				out = append(out, fields[i+1])
			}
			i++
		}
	}
	return out
}

// agentShellFields splits a joined command the way a shell would, quotes and
// escapes and all, so a body written '\{"model":"x"\}' reads as its JSON.
func agentShellFields(blob string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		start bool
	)
	flush := func() {
		if start {
			out = append(out, cur.String())
			cur.Reset()
			start = false
		}
	}
	rs := []rune(blob)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if quote != 0 {
			if r == '\\' && quote != '\'' && i+1 < len(rs) {
				i++
				cur.WriteRune(agentUnescape(rs[i], rs, &i))
				continue
			}
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			continue
		}
		switch {
		case r == '\\' && i+1 < len(rs):
			i++
			start = true
			cur.WriteRune(agentUnescape(rs[i], rs, &i))
		case r == '\'' || r == '"' || r == '`':
			quote, start = r, true
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			start = true
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// agentUnescape is one escaped character of a shell line.
func agentUnescape(r rune, rs []rune, i *int) rune {
	switch r {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case 'u':
		if *i+4 < len(rs) {
			n := 0
			for j := 1; j <= 4; j++ {
				d := agentHex(rs[*i+j])
				if d < 0 {
					return r
				}
				n = n*16 + d
			}
			*i += 4
			return rune(n)
		}
	}
	return r
}

func agentHex(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}

func dedupe(in []string) []string {
	var out []string
	for _, s := range in {
		if s == "" || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}
