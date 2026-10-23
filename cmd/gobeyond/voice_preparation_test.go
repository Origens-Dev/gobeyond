package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Origens-Dev/gobeyond/internal/project"
)

func preparationProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                 "module example.test/preparation\n\ngo 1.24\n",
		"app/page.tsx":           "export default function Page(){ return <p>Hello</p> }\n",
		"server/cmd/app/main.go": "package main\nfunc main(){}\n",
		"agents/operator/agent.go": `package operator
import agents "github.com/Origens-Dev/gobeyond/agents"
var Agent = agents.DefineAI(agents.AIConfig{Model:"google/gemini-2.5-flash", LiveModel:"gemini-live", ToolModel:"gemini-tool", VoiceName:"Kore", Durable:true, Realtime:true, Inference:"vertex"}, agents.Slots{Channels:[]agents.Channel{{ID:"voice", Connector:"assistant-line"}}})`,
		"agents/operator/instructions.md": "Private operator instructions.\n",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCopyVoicePreparationPrivateAndBound(t *testing.T) {
	root := preparationProject(t)
	if err := project.Write(root, nil, "build-test", false); err != nil {
		t.Fatal(err)
	}
	agents, err := project.LoadAgentsManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, ".gobeyond", "voice-preparation.json")
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "dist")
	destination := filepath.Join(dist, "deploy", "voice-preparation.json")
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyVoicePreparationManifest(root, dist, agents); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("private artifact changed: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private permissions: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dist, "static", "voice-preparation.json")); !os.IsNotExist(err) {
		t.Fatal("private artifact exposed as static asset")
	}
	for _, name := range []string{"build", "revision", "digest", "missing", "unknown", "trailing"} {
		t.Run(name, func(t *testing.T) {
			m, err := project.ReadVoicePreparationManifest(raw)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "build":
				m.BuildID = "other"
			case "revision":
				m.Agents[0].CompiledRevision = "other"
			case "digest":
				m.Agents[0].VoiceManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "missing":
				m.Agents = []project.VoicePreparationAgent{}
			}
			invalid, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if name == "unknown" {
				invalid = append(invalid[:len(invalid)-1], []byte(`,"unexpected":true}`)...)
			}
			if name == "trailing" {
				invalid = append(invalid, []byte(" {}")...)
			}
			if err := os.WriteFile(source, invalid, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readVoicePreparationManifest(root, agents); err == nil {
				t.Fatal("accepted mismatched private artifact")
			}
		})
	}
}

func TestTargetPlanVoiceInstructionsInvalidateReuse(t *testing.T) {
	root := preparationProject(t)
	if err := prepareTargetPlan(root); err != nil {
		t.Fatal(err)
	}
	first, err := readBuildCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTargetPlan(root); err != nil {
		t.Fatal(err)
	}
	same, err := readBuildCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.WebInputIdentity != same.WebInputIdentity {
		t.Fatal("unchanged private instructions changed cache identity")
	}
	if err := os.WriteFile(filepath.Join(root, "agents/operator/instructions.md"), []byte("Revised private instructions.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepareTargetPlan(root); err != nil {
		t.Fatal(err)
	}
	changed, err := readBuildCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if same.WebInputIdentity == changed.WebInputIdentity {
		t.Fatal("private instruction edit reused stale cache identity")
	}
}
