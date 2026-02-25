package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	arclient "github.com/agentregistry-dev/agentregistry/internal/client"
	"github.com/agentregistry-dev/agentregistry/pkg/models"
)

type resolvedSkillRef struct {
	name  string
	image string
}

func resolveSkillsForRuntime(manifest *models.AgentManifest) ([]resolvedSkillRef, error) {
	if manifest == nil || len(manifest.Skills) == 0 {
		return nil, nil
	}

	resolved := make([]resolvedSkillRef, 0, len(manifest.Skills))
	for _, skill := range manifest.Skills {
		imageRef, err := resolveSkillImageRef(skill)
		if err != nil {
			return nil, fmt.Errorf("resolve skill %q: %w", skill.Name, err)
		}
		resolved = append(resolved, resolvedSkillRef{
			name:  skill.Name,
			image: imageRef,
		})
	}
	slices.SortFunc(resolved, func(a, b resolvedSkillRef) int {
		return strings.Compare(a.name, b.name)
	})

	return resolved, nil
}

func resolveSkillImageRef(skill models.SkillRef) (string, error) {
	image := strings.TrimSpace(skill.Image)
	registrySkillName := strings.TrimSpace(skill.RegistrySkillName)
	hasImage := image != ""
	hasRegistry := registrySkillName != ""

	if !hasImage && !hasRegistry {
		return "", fmt.Errorf("one of image or registrySkillName is required")
	}
	if hasImage && hasRegistry {
		return "", fmt.Errorf("only one of image or registrySkillName may be set")
	}
	if hasImage {
		return image, nil
	}

	version := strings.TrimSpace(skill.RegistrySkillVersion)
	if version == "" {
		version = "latest"
	}

	skillResp, err := fetchSkillFromRegistry(skill.RegistryURL, registrySkillName, version)
	if err != nil {
		return "", err
	}
	if skillResp == nil {
		return "", fmt.Errorf("skill not found: %s (version %s)", registrySkillName, version)
	}

	imageRef, err := extractSkillImageRef(skillResp)
	if err != nil {
		return "", fmt.Errorf("skill %s (version %s): %w", registrySkillName, version, err)
	}
	return imageRef, nil
}

func fetchSkillFromRegistry(registryURL, skillName, version string) (*models.SkillResponse, error) {
	// Use the default configured API client when registry URL is omitted.
	if strings.TrimSpace(registryURL) == "" {
		if apiClient == nil {
			return nil, fmt.Errorf("API client not initialized")
		}
		if strings.EqualFold(version, "latest") {
			return apiClient.GetSkillByName(skillName)
		}
		return apiClient.GetSkillByNameAndVersion(skillName, version)
	}

	baseURL, err := normalizeSkillRegistryURL(registryURL)
	if err != nil {
		return nil, err
	}

	client := arclient.NewClient(baseURL, "")
	if strings.EqualFold(version, "latest") {
		resp, err := client.GetSkillByName(skillName)
		if err != nil {
			return nil, fmt.Errorf("fetch skill %q from %s: %w", skillName, baseURL, err)
		}
		return resp, nil
	}

	resp, err := client.GetSkillByNameAndVersion(skillName, version)
	if err != nil {
		return nil, fmt.Errorf("fetch skill %q version %q from %s: %w", skillName, version, baseURL, err)
	}
	return resp, nil
}

func normalizeSkillRegistryURL(registryURL string) (string, error) {
	trimmed := strings.TrimSpace(registryURL)
	if trimmed == "" {
		return "", fmt.Errorf("registry URL is required")
	}

	trimmed = strings.TrimSuffix(trimmed, "/")
	if strings.HasSuffix(trimmed, "/v0") {
		return trimmed, nil
	}
	return trimmed + "/v0", nil
}

func extractSkillImageRef(skillResp *models.SkillResponse) (string, error) {
	if skillResp == nil {
		return "", fmt.Errorf("skill response is required")
	}
	for _, pkg := range skillResp.Skill.Packages {
		typ := strings.ToLower(strings.TrimSpace(pkg.RegistryType))
		if (typ == "docker" || typ == "oci") && strings.TrimSpace(pkg.Identifier) != "" {
			return strings.TrimSpace(pkg.Identifier), nil
		}
	}
	return "", fmt.Errorf("no docker/oci package found")
}

func materializeSkillsForRuntime(ctx context.Context, skills []resolvedSkillRef, skillsDir string, verbose bool) error {
	if strings.TrimSpace(skillsDir) == "" {
		if len(skills) == 0 {
			return nil
		}
		return fmt.Errorf("skills directory is required")
	}

	if err := os.RemoveAll(skillsDir); err != nil {
		return fmt.Errorf("clear skills directory %s: %w", skillsDir, err)
	}
	if len(skills) == 0 {
		return nil
	}
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create skills directory %s: %w", skillsDir, err)
	}

	usedDirs := make(map[string]int)
	for _, skill := range skills {
		dirName := sanitizeSkillDirName(skill.name)
		if count := usedDirs[dirName]; count > 0 {
			dirName += "-" + strconv.Itoa(count+1)
		}
		usedDirs[dirName]++

		targetDir := filepath.Join(skillsDir, dirName)
		if err := extractSkillImage(ctx, skill.image, targetDir, verbose); err != nil {
			return fmt.Errorf("materialize skill %q from %q: %w", skill.name, skill.image, err)
		}
	}
	return nil
}

func sanitizeSkillDirName(name string) string {
	out := strings.TrimSpace(strings.ToLower(name))
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		" ", "-",
		".", "-",
		"@", "-",
	)
	out = replacer.Replace(out)
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		return "skill"
	}
	return out
}

func extractSkillImage(ctx context.Context, imageRef, targetDir string, verbose bool) error {
	if strings.TrimSpace(imageRef) == "" {
		return fmt.Errorf("image reference is required")
	}

	existsLocally, err := dockerImageExistsLocally(ctx, imageRef)
	if err != nil {
		return fmt.Errorf("check local image availability: %w", err)
	}
	if !existsLocally {
		if err := runDockerCommand(ctx, verbose, "pull", imageRef); err != nil {
			return fmt.Errorf("pull image: %w", err)
		}
	}

	containerID, err := createSkillContainer(ctx, imageRef)
	if err != nil {
		return err
	}
	defer func() {
		_ = runDockerCommand(ctx, false, "rm", containerID)
	}()

	tempDir, err := os.MkdirTemp("", "arctl-skill-extract-*")
	if err != nil {
		return fmt.Errorf("create temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	cpCmd := exec.CommandContext(ctx, "docker", "cp", containerID+":"+"/.", tempDir)
	if verbose {
		cpCmd.Stdout = os.Stdout
		cpCmd.Stderr = os.Stderr
		if err := cpCmd.Run(); err != nil {
			return fmt.Errorf("docker cp failed: %w", err)
		}
	} else {
		output, err := cpCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker cp failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target skill directory: %w", err)
	}

	if err := copyNonEmptyContents(tempDir, targetDir); err != nil {
		return fmt.Errorf("copy extracted skill contents: %w", err)
	}
	return nil
}

func createSkillContainer(ctx context.Context, imageRef string) (string, error) {
	createCmd := exec.CommandContext(ctx, "docker", "create", "--entrypoint", "/bin/sh", imageRef, "-c", "echo")
	output, err := createCmd.CombinedOutput()
	if err != nil {
		// Retry without entrypoint override for minimal images.
		fallback := exec.CommandContext(ctx, "docker", "create", imageRef)
		output, err = fallback.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("create container from image: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}

	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return "", fmt.Errorf("docker create returned empty container id")
	}
	return containerID, nil
}

func runDockerCommand(ctx context.Context, verbose bool, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func dockerImageExistsLocally(ctx context.Context, imageRef string) (bool, error) {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", imageRef)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
			return false, nil
		}
		return false, fmt.Errorf("docker image inspect failed: %w", err)
	}
	return true, nil
}

func copyNonEmptyContents(src, dst string) error {
	// Skip system directories created by Docker extraction from non-scratch images.
	skipDirs := map[string]struct{}{
		"dev":  {},
		"etc":  {},
		"proc": {},
		"sys":  {},
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read source directory: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if name == ".dockerenv" {
			continue
		}
		if _, skip := skipDirs[name]; skip {
			continue
		}

		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)

		if entry.IsDir() {
			if !hasNonEmptyContent(srcPath, skipDirs) {
				continue
			}
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return fmt.Errorf("create destination directory %s: %w", dstPath, err)
			}
			if err := copyNonEmptyContents(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat source file %s: %w", srcPath, err)
		}
		if info.Size() == 0 {
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("copy file %s: %w", srcPath, err)
		}
	}
	return nil
}

func hasNonEmptyContent(dir string, skipDirs map[string]struct{}) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		name := entry.Name()
		if name == ".dockerenv" {
			continue
		}
		if entry.IsDir() {
			if _, skip := skipDirs[name]; skip {
				continue
			}
			if hasNonEmptyContent(filepath.Join(dir, name), skipDirs) {
				return true
			}
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = destFile.Close() }()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, sourceInfo.Mode())
}
