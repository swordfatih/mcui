package app

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed policy/*.yaml
var policyFiles embed.FS

type dataRules struct {
	Bedrock []string `yaml:"bedrock"`
	Java    []string `yaml:"java"`
}
type compiledRules struct {
	bedrock []*regexp.Regexp
	java    []*regexp.Regexp
}
type dataPolicy struct {
	builtins   compiledRules
	persistent compiledRules
}

func loadDataPolicy() (dataPolicy, error) {
	load := func(name string) (compiledRules, error) {
		content, err := policyFiles.ReadFile("policy/" + name + ".yaml")
		if err != nil {
			return compiledRules{}, err
		}
		var rules dataRules
		if err := yaml.Unmarshal(content, &rules); err != nil {
			return compiledRules{}, err
		}
		compile := func(patterns []string) ([]*regexp.Regexp, error) {
			out := make([]*regexp.Regexp, 0, len(patterns))
			for _, pattern := range patterns {
				if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") {
					return nil, errors.New("data policy patterns must be anchored")
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return nil, err
				}
				out = append(out, re)
			}
			return out, nil
		}
		bedrock, err := compile(rules.Bedrock)
		if err != nil {
			return compiledRules{}, err
		}
		java, err := compile(rules.Java)
		return compiledRules{bedrock, java}, err
	}
	builtins, err := load("builtins")
	if err != nil {
		return dataPolicy{}, err
	}
	persistent, err := load("persistent")
	return dataPolicy{builtins, persistent}, err
}

var serverDataPolicy = func() dataPolicy {
	policy, err := loadDataPolicy()
	if err != nil {
		panic(err)
	}
	return policy
}()

func (r compiledRules) match(edition, path string) bool {
	patterns := r.java
	if edition == "bedrock" {
		patterns = r.bedrock
	}
	for _, re := range patterns {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

func (p dataPolicy) keep(edition, rel string) bool {
	if p.builtins.match(edition, rel) {
		return false
	}
	if p.persistent.match(edition, rel) {
		return true
	}
	return false
}

func keepDataPath(root, edition, rel string) bool {
	if serverDataPolicy.keep(edition, rel) {
		return true
	}
	if edition != "java" || serverDataPolicy.builtins.match(edition, rel) {
		return false
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		world := filepath.Join(append([]string{root}, parts[:i]...)...)
		info, err := os.Lstat(filepath.Join(world, "level.dat"))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

type resetEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}
type resetPlan struct {
	Keep       []resetEntry `json:"keep"`
	Delete     []resetEntry `json:"delete"`
	DeleteDirs []string     `json:"deleteDirs"`
	Revision   string       `json:"revision"`
}

func planData(root, edition string) (resetPlan, error) {
	plan := resetPlan{Keep: []resetEntry{}, Delete: []resetEntry{}, DeleteDirs: []string{}}
	var directories []string
	hash := sha256.New()
	fmt.Fprintf(hash, "edition\x00%s\n", edition)
	info, err := os.Lstat(root)
	if err != nil {
		return plan, err
	}
	if !info.IsDir() {
		return plan, errors.New("server data is not a directory")
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in server data: " + rel)
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("special file in server data: " + rel)
		}
		if entry.IsDir() {
			fmt.Fprintf(hash, "d\x00%s\n", rel)
			directories = append(directories, rel)
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "f\x00%s\x00%d\x00%d\x00%d\n", rel, fileInfo.Size(), fileInfo.ModTime().UnixNano(), fileInfo.Mode())
		item := resetEntry{rel, fileInfo.Size()}
		if keepDataPath(root, edition, rel) {
			fmt.Fprint(hash, "keep\n")
			plan.Keep = append(plan.Keep, item)
		} else {
			fmt.Fprint(hash, "delete\n")
			plan.Delete = append(plan.Delete, item)
		}
		return nil
	})
	if err != nil {
		return plan, err
	}
	sort.Slice(plan.Keep, func(i, j int) bool { return plan.Keep[i].Path < plan.Keep[j].Path })
	sort.Slice(plan.Delete, func(i, j int) bool { return plan.Delete[i].Path < plan.Delete[j].Path })
	keptDirs := make(map[string]bool)
	for _, item := range plan.Keep {
		for dir := filepath.ToSlash(filepath.Dir(item.Path)); dir != "."; dir = filepath.ToSlash(filepath.Dir(dir)) {
			keptDirs[dir] = true
		}
	}
	for _, dir := range directories {
		if !keptDirs[dir] {
			plan.DeleteDirs = append(plan.DeleteDirs, dir)
		}
	}
	sort.Slice(plan.DeleteDirs, func(i, j int) bool { return plan.DeleteDirs[i] > plan.DeleteDirs[j] })
	plan.Revision = hex.EncodeToString(hash.Sum(nil))
	return plan, nil
}
