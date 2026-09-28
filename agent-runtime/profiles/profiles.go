// Package profiles embeds the same product prompts shipped in the Runtime image.
package profiles

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"

	"github.com/bytedance/sonic"
)

//go:embed p0.json
var defaults []byte

type Definition struct {
	ID, SystemPrompt, Revision, PromptBundleDigest, SkillsDigest string
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// Defaults returns a detached copy; callers cannot change the embedded prompts.
func Defaults() map[string]Definition {
	var prompts map[string]string
	if err := sonic.Unmarshal(defaults, &prompts); err != nil {
		panic("agent_profile_defaults_invalid")
	}
	out := make(map[string]Definition, len(prompts))
	for id, prompt := range prompts {
		out[id] = Definition{ID: id, SystemPrompt: prompt, Revision: digest(id + "\n" + prompt),
			PromptBundleDigest: digest(prompt), SkillsDigest: digest("[]")}
	}
	return out
}
