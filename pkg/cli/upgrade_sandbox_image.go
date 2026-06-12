package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ethpandaops/panda/internal/version"
)

// sandboxPinOutcome describes what pinning sandbox.image in the server config
// would (or did) do.
type sandboxPinOutcome int

const (
	// sandboxPinUpdated: the image line was (or would be) rewritten.
	sandboxPinUpdated sandboxPinOutcome = iota
	// sandboxPinAlreadyPinned: the config already references the target image.
	sandboxPinAlreadyPinned
	// sandboxPinCustomImage: the config references an image panda does not
	// manage (different repo or non-sandbox tag); left untouched.
	sandboxPinCustomImage
	// sandboxPinNotApplicable: no config file, no sandbox image line, or the
	// running binary has no concrete release version to pin to.
	sandboxPinNotApplicable
)

// managedSandboxImagePrefix identifies sandbox images published by panda
// releases. Only these are rewritten on upgrade — anything else is a
// deliberate user override (e.g. a locally built image).
const managedSandboxImagePrefix = imageRepo + ":sandbox-"

// sandboxImageLine matches an "image:" mapping line, capturing the
// indentation, the value, and any trailing comment.
var sandboxImageLine = regexp.MustCompile(`^(\s+image:\s*)("[^"]*"|'[^']*'|[^\s#]+)(\s*(?:#.*)?)$`)

// serverConfigPath returns the server config file that the compose file
// bind-mounts into the server container.
func serverConfigPath() string {
	return filepath.Join(filepath.Dir(resolveComposeFile()), "config.yaml")
}

// pinSandboxImageInConfig rewrites sandbox.image in the server config to the
// sandbox image pinned to the given version. The compose regeneration pins
// the server image, but sandbox.image lives in config.yaml — without this,
// upgraded servers keep launching sessions from a stale sandbox image (and
// floating tags like sandbox-latest silently lag behind pre-releases).
//
// Only panda-published images (ethpandaops/panda:sandbox-*) are rewritten;
// any other value is a deliberate user override and is left alone. With
// dryRun the file is not modified — used to print an honest upgrade plan.
func pinSandboxImageInConfig(targetVersion string, dryRun bool) (sandboxPinOutcome, string, error) {
	target := sandboxImageForVersion(targetVersion)
	if target == defaultSandboxImage {
		// No concrete release version (dev build): pinning would rewrite a
		// concrete pin back to a floating tag.
		return sandboxPinNotApplicable, "", nil
	}

	configPath := serverConfigPath()

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return sandboxPinNotApplicable, "", nil
		}

		return sandboxPinNotApplicable, "", fmt.Errorf("reading %s: %w", configPath, err)
	}

	updated, outcome, current := rewriteSandboxImage(string(data), target)
	if outcome != sandboxPinUpdated {
		return outcome, current, nil
	}

	if dryRun {
		return sandboxPinUpdated, current, nil
	}

	info, err := os.Stat(configPath)
	if err != nil {
		return sandboxPinNotApplicable, "", fmt.Errorf("stat %s: %w", configPath, err)
	}

	if err := os.WriteFile(configPath, []byte(updated), info.Mode().Perm()); err != nil {
		return sandboxPinNotApplicable, "", fmt.Errorf("writing %s: %w", configPath, err)
	}

	return sandboxPinUpdated, current, nil
}

// rewriteSandboxImage rewrites the image line inside the top-level sandbox
// block of a server config document, preserving indentation, surrounding
// content, and any trailing comment. It returns the rewritten document, the
// outcome, and the previous image value.
func rewriteSandboxImage(doc, target string) (string, sandboxPinOutcome, string) {
	lines := strings.Split(doc, "\n")
	inSandbox := false

	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")

		// A non-indented, non-comment "key:" line starts a new top-level block.
		if trimmed != "" && trimmed[0] != ' ' && trimmed[0] != '\t' && trimmed[0] != '#' {
			inSandbox = trimmed == "sandbox:" || strings.HasPrefix(trimmed, "sandbox:")
			continue
		}

		if !inSandbox {
			continue
		}

		match := sandboxImageLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		current := strings.Trim(match[2], `"'`)

		if current == target {
			return doc, sandboxPinAlreadyPinned, current
		}

		if !strings.HasPrefix(current, managedSandboxImagePrefix) {
			return doc, sandboxPinCustomImage, current
		}

		lines[i] = fmt.Sprintf("%s%q%s", match[1], target, match[3])

		return strings.Join(lines, "\n"), sandboxPinUpdated, current
	}

	return doc, sandboxPinNotApplicable, ""
}

// pinSandboxImage applies the sandbox image pin for the running binary's
// version, reporting what happened. Failures are warnings: a config the
// process cannot rewrite must not abort the rest of the server upgrade.
func pinSandboxImage() {
	outcome, current, err := pinSandboxImageInConfig(version.Version, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to pin sandbox image in config: %v\n", err)
		return
	}

	switch outcome {
	case sandboxPinUpdated:
		fmt.Printf("Pinned sandbox image in %s (%s -> %s)\n",
			serverConfigPath(), current, sandboxImageForVersion(version.Version))
	case sandboxPinCustomImage:
		fmt.Printf("Leaving custom sandbox image unchanged: %s\n", current)
	case sandboxPinAlreadyPinned, sandboxPinNotApplicable:
	}
}
