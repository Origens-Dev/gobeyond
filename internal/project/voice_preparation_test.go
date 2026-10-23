package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preparationFixtureDefinitions() []AgentDefinition {
	return []AgentDefinition{{ID: "operator", Kind: AgentKindAI, Durable: true, Realtime: true, Revision: "revision-1", Instructions: "  Exact compiled instructions.\nSecond line.\n", Slots: AgentSlots{Channels: []AgentChannel{{ID: "voice"}}}}}
}
func preparationFixtureManifest(t *testing.T, defs []AgentDefinition) AgentsManifest {
	t.Helper()
	m := portableAgentsManifest(defs, "build-1")
	if err := attachVoiceManifests(&m, defs); err != nil {
		t.Fatal(err)
	}
	return m
}
func TestCompiledVoicePreparationExactFinalizedIdentity(t *testing.T) {
	defs := preparationFixtureDefinitions()
	m := preparationFixtureManifest(t, defs)
	got, err := compiledVoicePreparation(defs, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 || got.Agents[0].BaseInstructions != defs[0].Instructions || got.Agents[0].CompiledRevision != defs[0].Revision || got.Agents[0].VoiceManifestDigest != m.Agents[0].VoiceManifestDigest {
		t.Fatalf("artifact lost compiled identity %+v", got)
	}
	source, err := generatedAgentRegistration(AgentDefinition{ID: defs[0].ID, Kind: AgentKindAI, PackageName: "operator", Instructions: defs[0].Instructions, Revision: defs[0].Revision})
	if err != nil || !bytes.Contains(source, []byte(fmt.Sprintf("definition.AI.Instructions = %q", got.Agents[0].BaseInstructions))) || !bytes.Contains(source, []byte(fmt.Sprintf("definition.AI.Revision = %q", got.Agents[0].CompiledRevision))) {
		t.Fatalf("artifact differs from generated runtime %s %v", source, err)
	}
	old, _ := json.Marshal(got)
	defs[0].Instructions = "Revised exact instruction bytes."
	defs[0].Revision = "revision-2"
	updated, err := compiledVoicePreparation(defs, preparationFixtureManifest(t, defs))
	if err != nil {
		t.Fatal(err)
	}
	now, _ := json.Marshal(updated)
	if bytes.Equal(old, now) || updated.Agents[0].VoiceManifestDigest == got.Agents[0].VoiceManifestDigest {
		t.Fatal("revised compiled artifact stayed stale")
	}
}
func TestVoicePreparationEligibilityAndBindings(t *testing.T) {
	defs := preparationFixtureDefinitions()
	for _, d := range []AgentDefinition{{ID: "direct", Kind: AgentKindAI, Instructions: "direct", Revision: "rev"}, {ID: "durable", Kind: AgentKindAI, Durable: true, Instructions: "durable", Revision: "rev"}, {ID: "handler", Kind: AgentKindHandler, Durable: true, Realtime: true, Revision: "rev"}} {
		defs = append(defs, d)
	}
	m := preparationFixtureManifest(t, defs)
	got, err := compiledVoicePreparation(defs, m)
	if err != nil || len(got.Agents) != 1 {
		t.Fatalf("eligibility %+v %v", got, err)
	}
	m.Agents[0].Revision = "substituted"
	if _, err := compiledVoicePreparation(defs, m); err == nil {
		t.Fatal("revision substitution accepted")
	}
	defs = preparationFixtureDefinitions()
	defs = append(defs, defs[0])
	if _, err := compiledVoicePreparation(defs, preparationFixtureManifest(t, defs)); err == nil {
		t.Fatal("duplicate definitions accepted")
	}
}
func TestPrivateVoicePreparationReadBoundsAndStrictShape(t *testing.T) {
	defs := preparationFixtureDefinitions()
	m, err := compiledVoicePreparation(defs, preparationFixtureManifest(t, defs))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if _, err := ReadVoicePreparationManifest(raw); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*VoicePreparationManifest){"version": func(m *VoicePreparationManifest) { m.Version = 2 }, "build": func(m *VoicePreparationManifest) { m.BuildID = "" }, "duplicate": func(m *VoicePreparationManifest) { m.Agents = append(m.Agents, m.Agents[0]) }, "empty instructions": func(m *VoicePreparationManifest) { m.Agents[0].BaseInstructions = " \n" }, "oversized instructions": func(m *VoicePreparationManifest) {
		m.Agents[0].BaseInstructions = strings.Repeat("x", MaxVoicePreparationInstructionsBytes+1)
	}, "digest": func(m *VoicePreparationManifest) { m.Agents[0].VoiceManifestDigest = "sha256:invalid" }, "nil entries": func(m *VoicePreparationManifest) { m.Agents = nil }} {
		t.Run(name, func(t *testing.T) {
			var bad VoicePreparationManifest
			_ = json.Unmarshal(raw, &bad)
			mutate(&bad)
			if ValidateVoicePreparationManifest(bad) == nil {
				t.Fatal("invalid private artifact accepted")
			}
		})
	}
	unknownField := []byte(`{"version":1,"buildId":"build","agents":[],"extra":true}`)
	if !json.Valid(unknownField) {
		t.Fatal("unknown field fixture must be valid JSON")
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), []byte(` {}`)...), unknownField, bytes.Repeat([]byte("x"), MaxVoicePreparationBytes+1)} {
		if _, err := ReadVoicePreparationManifest(bad); err == nil {
			t.Fatal("invalid wire accepted")
		}
	}
}
func TestWritePrivateVoicePreparationMatchesGeneratedRuntimeAndCheck(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeSourceTestFile(t, filepath.Join(root, "agents", "operator", "agent.go"), `package operator
import "github.com/Origens-Dev/gobeyond/agents"
var Agent=agents.DefineAI(agents.AIConfig{Model:"google/gemini-2.5-flash",LiveModel:"gemini-live",ToolModel:"gemini-tool",Durable:true,Realtime:true},agents.Slots{Channels:[]agents.Channel{{ID:"voice"}}})
`)
	writeSourceTestFile(t, filepath.Join(root, "agents", "operator", "instructions.md"), "First private instructions.\n")
	for n := 1; n <= 2; n++ {
		if n == 2 {
			if err := os.Chmod(filepath.Join(root, ".gobeyond", "voice-preparation.json"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		instructions := fmt.Sprintf("Private exact instructions %d.\nSecond line.", n)
		writeSourceTestFile(t, filepath.Join(root, "agents", "operator", "instructions.md"), instructions)
		revision := fmt.Sprintf("revision-%d", n)
		build := fmt.Sprintf("build-%d", n)
		if err := WriteWithAgentRevisions(root, nil, build, false, map[string]string{"operator": revision}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(root, ".gobeyond", "voice-preparation.json"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := ReadVoicePreparationManifest(raw)
		if err != nil || m.BuildID != build || len(m.Agents) != 1 || m.Agents[0].CompiledRevision != revision || m.Agents[0].BaseInstructions != instructions {
			t.Fatalf("private artifact %+v %v", m, err)
		}
		source, err := os.ReadFile(filepath.Join(root, GeneratedDir, "agents", AgentDefinitionKey("operator"), "gobeyond_register_gen.go"))
		if err != nil || !bytes.Contains(source, []byte(fmt.Sprintf("definition.AI.Instructions = %q", m.Agents[0].BaseInstructions))) || !bytes.Contains(source, []byte(fmt.Sprintf("definition.AI.Revision = %q", revision))) {
			t.Fatalf("runtime identity differs %v", err)
		}
		publishedRaw, err := os.ReadFile(filepath.Join(root, ".gobeyond", "agents.json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(publishedRaw, []byte(instructions)) || bytes.Contains(publishedRaw, []byte("baseInstructions")) {
			t.Fatal("instructions leaked into public agent metadata")
		}
		var published AgentsManifest
		if json.Unmarshal(publishedRaw, &published) != nil {
			t.Fatal("agent manifest decode")
		}
		_, digest, err := voicecontract.FreezeManifest(*published.Agents[0].VoiceManifest)
		if err != nil || digest != m.Agents[0].VoiceManifestDigest {
			t.Fatal("manifest digest mismatched")
		}
		info, _ := os.Stat(filepath.Join(root, ".gobeyond", "voice-preparation.json"))
		if info.Mode().Perm() != 0600 {
			t.Fatal("private artifact permissions")
		}
		if err := os.Remove(filepath.Join(root, ".gobeyond", "voice-preparation.json")); err != nil {
			t.Fatal(err)
		}
		if err := WriteWithAgentRevisions(root, nil, build, true, map[string]string{"operator": revision}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, ".gobeyond", "voice-preparation.json")); err != nil {
			t.Fatal("check did not materialize private input")
		}
	}
}

func TestVoicePreparationRejectsDuplicateAliasAndInvalidUTF8(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`{"version":1,"version":1,"buildId":"build","agents":[]}`), []byte(`{"version":1,"BuildId":"build","agents":[]}`), []byte(`{"version":1,"buildId":"build","agents":[{"agentId":"a","agentId":"a","compiledRevision":"rev","voiceManifestDigest":"sha256:` + strings.Repeat("a", 64) + `","baseInstructions":"text"}]}`), []byte(`{"version":1,"buildId":"build","agents":[{"agentId":"a","CompiledRevision":"rev","voiceManifestDigest":"sha256:` + strings.Repeat("a", 64) + `","baseInstructions":"text"}]}`)} {
		if !json.Valid(raw) {
			t.Fatalf("negative fixture must begin as valid JSON: %q", raw)
		}
		if _, err := ReadVoicePreparationManifest(raw); err == nil {
			t.Fatalf("malformed wire accepted %q", raw)
		}
	}
}

func TestPrivateVoicePreparationRejectsMalformedUTF8InsideJSONString(t *testing.T) {
	raw := []byte(`{"version":1,"buildId":"build","agents":[{"agentId":"a","compiledRevision":"rev","voiceManifestDigest":"sha256:` + strings.Repeat("a", 64) + `","baseInstructions":"textPLACEHOLDER"}]}`)
	if !json.Valid(raw) {
		t.Fatal("UTF8 fixture baseline must be valid JSON")
	}
	if _, err := ReadVoicePreparationManifest(raw); err != nil {
		t.Fatalf("UTF8 fixture baseline must be accepted: %v", err)
	}
	raw = bytes.Replace(raw, []byte("PLACEHOLDER"), []byte{0xff}, 1)
	if _, err := ReadVoicePreparationManifest(raw); err == nil {
		t.Fatal("invalid UTF8 normalized instead of rejected")
	}
}
