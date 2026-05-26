// Command import-nailforge ingests a NailForge `warehouse/` tree into a
// Box (the `nail-index` box by default) incrementally — first run creates,
// subsequent runs skip unchanged files and replace items whose content hash
// has shifted. YAML parsing is intentionally confined to this command so the
// box/* core never picks up a YAML dependency (R0.6.2 architect decision).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/windborneos/box-model/box"
	"sigs.k8s.io/yaml"
)

// config captures the CLI surface so tests can drive run() directly.
type config struct {
	warehouse string
	boxKey    string
	boxHome   string
	owner     string
	dryRun    bool
	verbose   bool
}

// stats is the per-invocation tally surfaced in the final report and assertable
// by tests via stdout.
type stats struct {
	total    int
	created  int
	updated  int
	skipped  int
	errors   int
	errorLog []string
}

// existingRef is the in-memory mirror of an item already in the target Box,
// keyed by idem_key. We carry id (for ReplaceItem), hash (for the skip-vs-
// update decision), and the existing symbols (so we can detect when symbols
// alone need re-seeding — see R0.7.6's "hash-equal but symbols-diff" branch).
type existingRef struct {
	itemID      string
	contentHash string
	symbols     []box.Symbol
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		// help/usage already written to stderr by flag package
		os.Exit(2)
	}
	if err := run(cfg, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("import-nailforge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg config
	fs.StringVar(&cfg.warehouse, "warehouse", "", "Path to NailForge warehouse directory (required)")
	fs.StringVar(&cfg.boxKey, "box-key", "nail-index", "Target box key")
	fs.StringVar(&cfg.boxHome, "box-home", "", "Box home directory (defaults to $BOX_HOME or ~/.box)")
	fs.StringVar(&cfg.owner, "owner", "trine", "Owner ID when creating a new box")
	fs.BoolVar(&cfg.dryRun, "dry-run", false, "Report what would happen without writing")
	fs.BoolVar(&cfg.verbose, "verbose", false, "Print per-file outcome")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.warehouse == "" {
		fmt.Fprintln(stderr, "Error: --warehouse is required")
		fs.Usage()
		return cfg, errors.New("missing --warehouse")
	}
	return cfg, nil
}

// resolveBoxHome mirrors cli/cli.go's --root > $BOX_HOME > ~/.box ladder.
func resolveBoxHome(cfg config) (string, error) {
	if cfg.boxHome != "" {
		return cfg.boxHome, nil
	}
	if v := os.Getenv("BOX_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".box"), nil
}

// run is the test-facing entrypoint: streams progress + the final report
// into stdout, returns non-nil on fatal errors (failure to open store,
// failure to walk warehouse). Per-file failures are recorded in stats and
// do not abort the run.
func run(cfg config, stdout, stderr io.Writer) error {
	if cfg.warehouse == "" {
		return errors.New("warehouse is required")
	}
	root, err := resolveBoxHome(cfg)
	if err != nil {
		return fmt.Errorf("resolve box-home: %w", err)
	}
	fs, err := box.OpenFileStore(root)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer fs.Close()
	svc := box.NewService(fs)
	ctx := context.Background()

	gitSHA := readGitSHA(cfg.warehouse)

	var b box.Box
	var existing map[string]existingRef
	if cfg.dryRun {
		// In dry-run mode we never write; resolving the box (if it happens to
		// exist already) lets us still compare against existing hashes. If the
		// box is absent we treat the inventory as empty.
		b, err = svc.GetBoxByKey(ctx, cfg.owner, cfg.boxKey)
		if err != nil && !errors.Is(err, box.ErrNotFound) {
			return fmt.Errorf("get box: %w", err)
		}
		existing = map[string]existingRef{}
		if err == nil {
			existing, err = loadExistingItems(ctx, svc, b.ID)
			if err != nil {
				return fmt.Errorf("load existing: %w", err)
			}
		}
	} else {
		b, err = ensureBox(ctx, svc, cfg)
		if err != nil {
			return fmt.Errorf("ensure box: %w", err)
		}
		existing, err = loadExistingItems(ctx, svc, b.ID)
		if err != nil {
			return fmt.Errorf("load existing: %w", err)
		}
	}

	st := &stats{}
	walkErr := filepath.Walk(cfg.warehouse, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			st.errors++
			st.errorLog = append(st.errorLog, fmt.Sprintf("walk %s: %v", path, walkErr))
			return nil
		}
		if info.IsDir() {
			if path != cfg.warehouse && shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		action, perr := processFile(ctx, svc, cfg, b, gitSHA, path, existing)
		st.total++
		switch {
		case perr != nil:
			st.errors++
			st.errorLog = append(st.errorLog, fmt.Sprintf("%s: %v", path, perr))
			if cfg.verbose {
				fmt.Fprintf(stdout, "ERROR  %s: %v\n", path, perr)
			}
		case action == "created":
			st.created++
			if cfg.verbose {
				fmt.Fprintf(stdout, "CREATE %s\n", path)
			}
		case action == "updated":
			st.updated++
			if cfg.verbose {
				fmt.Fprintf(stdout, "UPDATE %s\n", path)
			}
		case action == "skipped":
			st.skipped++
			if cfg.verbose {
				fmt.Fprintf(stdout, "SKIP   %s\n", path)
			}
		case action == "ignored":
			// File outside known taxonomy (e.g. yaml in unexpected location);
			// counts toward total walked but neither created nor errored.
			st.total-- // not really a target; do not double-count
		}
		return nil
	})

	fmt.Fprintf(stdout, "import-nailforge: warehouse=%s box=%s git_sha=%q dry_run=%v\n",
		cfg.warehouse, cfg.boxKey, gitSHA, cfg.dryRun)
	fmt.Fprintf(stdout, "total=%d created=%d updated=%d skipped=%d errors=%d\n",
		st.total, st.created, st.updated, st.skipped, st.errors)
	if len(st.errorLog) > 0 {
		fmt.Fprintln(stdout, "errors:")
		for _, line := range st.errorLog {
			fmt.Fprintf(stdout, "  - %s\n", line)
		}
	}
	if walkErr != nil {
		return fmt.Errorf("walk: %w", walkErr)
	}
	return nil
}

// ensureBox returns the target Box, creating it on first run with the
// architect-mandated storage policy (max 100k items, 1MB content, yaml+json).
func ensureBox(ctx context.Context, svc *box.Service, cfg config) (box.Box, error) {
	b, err := svc.GetBoxByKey(ctx, cfg.owner, cfg.boxKey)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, box.ErrNotFound) {
		return box.Box{}, err
	}
	return svc.CreateBox(ctx, box.CreateBoxRequest{
		Key:       cfg.boxKey,
		OwnerType: "user",
		OwnerID:   cfg.owner,
		StoragePolicy: box.StoragePolicy{
			AllowedFormats:  []string{"json", "yaml"},
			MaxItems:        100000,
			MaxContentBytes: 1024 * 1024,
		},
		Labels: map[string]string{"__op:project": "nailforge"},
	})
}

// readGitSHA tries `git -C <dir> rev-parse HEAD`. Failures (not a repo, git
// missing) collapse to "" — storage_uri then falls back to folder://.
func readGitSHA(warehouseDir string) string {
	dir := filepath.Dir(warehouseDir) // warehouse lives inside the repo root
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// loadExistingItems pulls every (latest) item in the box into an in-memory
// idem_key -> {id, hash} map for the incremental diff.
func loadExistingItems(ctx context.Context, svc *box.Service, boxID string) (map[string]existingRef, error) {
	items, err := svc.Browse(ctx, boxID, box.BrowseFilter{Limit: 100000})
	if err != nil {
		return nil, err
	}
	out := make(map[string]existingRef, len(items))
	for _, it := range items {
		out[it.IdemKey] = existingRef{itemID: it.ID, contentHash: it.ContentHash, symbols: it.Symbols}
	}
	return out, nil
}

// processFile reads, classifies, and either stores/replaces/skips a single
// YAML file. The returned action is one of "created"/"updated"/"skipped"/
// "ignored".
func processFile(
	ctx context.Context,
	svc *box.Service,
	cfg config,
	b box.Box,
	gitSHA string,
	path string,
	existing map[string]existingRef,
) (string, error) {
	yamlBytes, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return "", fmt.Errorf("yaml->json: %w", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		// Non-object YAML (e.g. a scalar or a list) — bail; we never index it.
		return "ignored", nil
	}
	relPath, err := filepath.Rel(cfg.warehouse, path)
	if err != nil {
		return "", fmt.Errorf("rel: %w", err)
	}
	kind, idemKey, library, family, nailFilename, version := classify(relPath, parsed)
	if kind == "" {
		return "ignored", nil
	}

	labels := extractLabels(parsed, library, family, kind)

	sourceRef := map[string]string{
		"library":   library,
		"family":    family,
		"nail":      nailFilename,
		"version":   version,
		"yaml_path": relPath,
	}
	if gitSHA != "" {
		sourceRef["git_sha"] = gitSHA
	}

	var storageURI string
	if gitSHA != "" {
		storageURI = fmt.Sprintf("repo://nailforge@%s:/warehouse/%s", gitSHA, relPath)
	} else {
		abs, _ := filepath.Abs(path)
		storageURI = "folder://" + abs
	}

	newHash := box.ContentHash(json.RawMessage(jsonBytes))
	prev, present := existing[idemKey]

	if cfg.dryRun {
		switch {
		case !present:
			return "created", nil
		case prev.contentHash == newHash:
			return "skipped", nil
		default:
			return "updated", nil
		}
	}

	symbols := buildItemSymbols(parsed, kind)

	req := box.StoreRequest{
		IdemKey:    idemKey,
		Kind:       kind,
		SourceType: "yaml",
		SourceRef:  sourceRef,
		Labels:     labels,
		StorageURI: storageURI,
		Format:     "yaml",
		Content:    json.RawMessage(jsonBytes),
		StoredBy:   b.OwnerID,
		Symbols:    symbols,
	}

	switch {
	case !present:
		item, err := svc.Store(ctx, b.OwnerID, b.ID, req)
		if err != nil {
			return "", fmt.Errorf("store: %w", err)
		}
		existing[idemKey] = existingRef{itemID: item.ID, contentHash: item.ContentHash, symbols: item.Symbols}
		return "created", nil
	case prev.contentHash == newHash:
		if symbolsEqual(prev.symbols, symbols) {
			return "skipped", nil
		}
		// Content unchanged but the symbol layer needs refreshing (R0.7.6
		// migration path: existing items pre-date the symbol engine).
		item, err := svc.ReplaceItem(ctx, b.OwnerID, prev.itemID, req)
		if err != nil {
			return "", fmt.Errorf("replace (symbols): %w", err)
		}
		existing[idemKey] = existingRef{itemID: item.ID, contentHash: item.ContentHash, symbols: item.Symbols}
		return "updated", nil
	default:
		item, err := svc.ReplaceItem(ctx, b.OwnerID, prev.itemID, req)
		if err != nil {
			return "", fmt.Errorf("replace: %w", err)
		}
		existing[idemKey] = existingRef{itemID: item.ID, contentHash: item.ContentHash, symbols: item.Symbols}
		return "updated", nil
	}
}

// shouldSkipDir lists subdirectories under a family that hold non-index
// artifacts (build outputs, lifecycle bins, python caches).
func shouldSkipDir(name string) bool {
	switch name {
	case "output", "active", "deprecated", "draft", "newborn", "retired", "__pycache__":
		return true
	}
	return false
}

// classify maps a relative path (and the parsed YAML map) to a Box (kind,
// idem_key) plus the path segments callers need for source_ref / labels.
// An empty kind means "ignore this file".
func classify(relPath string, parsed map[string]any) (kind, idemKey, library, family, nailFilename, version string) {
	parts := strings.Split(relPath, string(filepath.Separator))
	if len(parts) < 3 {
		// We expect <library>/<family>/<file or subdir/file>.
		return "", "", "", "", "", ""
	}
	library = parts[0]
	family = parts[1]
	filename := parts[len(parts)-1]

	switch {
	case len(parts) == 3 && filename == "family_map.yaml":
		return "nail_family",
			fmt.Sprintf("nailforge/%s/%s/family_map", library, family),
			library, family, "", ""
	case len(parts) == 3 && filename == "lineage.yaml":
		return "nail_lineage",
			fmt.Sprintf("nailforge/%s/%s/lineage", library, family),
			library, family, "", ""
	case len(parts) == 4 && parts[2] == "versions":
		stem := strings.TrimSuffix(filename, ".yaml")
		nail := stem
		ver := ""
		// stem looks like "<nail_id>.<version>", e.g. "data_lake_organizer.S0.1-E0-A0-L1".
		// We treat ".S" as the version-tag boundary.
		if idx := strings.Index(stem, ".S"); idx > 0 {
			nail = stem[:idx]
			ver = stem[idx+1:]
		}
		nailType := inferNailType(parsed)
		switch nailType {
		case "organ_nail":
			kind = "organ_nail"
		case "action_nail":
			kind = "action_nail"
		default:
			kind = "nail_version"
		}
		return kind,
			fmt.Sprintf("nailforge/%s/%s/nails/%s", library, family, stem),
			library, family, nail, ver
	}
	return "", "", "", "", "", ""
}

// inferNailType peeks at the parsed YAML for the nail's declared type. We try
// nail.type, then top-level type. Returns "" if absent / wrong shape.
func inferNailType(parsed map[string]any) string {
	if nail, ok := parsed["nail"].(map[string]any); ok {
		if t, ok := nail["type"].(string); ok {
			return t
		}
	}
	if t, ok := parsed["type"].(string); ok {
		return t
	}
	return ""
}

// extractLabels packs the standard label set for an indexed item. Any
// extraction failure is silent — the resulting label is simply omitted.
func extractLabels(parsed map[string]any, library, family, kind string) map[string]string {
	labels := map[string]string{
		"__op:library": library,
		"__op:family":  family,
	}
	switch kind {
	case "nail_family":
		labels["__sem:nail_type"] = "family_map"
	case "nail_lineage":
		labels["__sem:nail_type"] = "lineage"
	case "action_nail":
		labels["__sem:nail_type"] = "action"
	case "organ_nail":
		labels["__sem:nail_type"] = "organ"
	case "nail_version":
		labels["__sem:nail_type"] = "version"
	}

	// parent_organ / parent_petal come from family.* on family_map and nail
	// version files.
	if fam, ok := parsed["family"].(map[string]any); ok {
		if v, ok := fam["parent_organ"].(string); ok && v != "" {
			labels["__sem:parent_organ"] = v
		}
		if v, ok := fam["parent_petal"].(string); ok && v != "" {
			labels["__sem:parent_petal"] = v
		}
	}
	if nail, ok := parsed["nail"].(map[string]any); ok {
		if fam, ok := nail["family"].(map[string]any); ok {
			if v, ok := fam["parent_organ"].(string); ok && v != "" {
				labels["__sem:parent_organ"] = v
			}
			if v, ok := fam["parent_petal"].(string); ok && v != "" {
				labels["__sem:parent_petal"] = v
			}
		}
	}

	// atom verb extraction for action/organ nails. The atom field is either a
	// string ("构(extract, ...)") or an object ({method: extract, ...}).
	if kind == "action_nail" || kind == "organ_nail" {
		if nail, ok := parsed["nail"].(map[string]any); ok {
			if atomStr, ok := nail["atom"].(string); ok {
				if v := extractAtomVerb(atomStr); v != "" {
					labels["__sem:atom"] = v
				}
			} else if atomMap, ok := nail["atom"].(map[string]any); ok {
				if v, ok := atomMap["method"].(string); ok && v != "" {
					labels["__sem:atom"] = v
				}
			}
		}
	}

	// status: deprecated takes precedence over active. We honour
	// family.deprecated and family.lifecycle.status (string).
	status := "active"
	if fam, ok := parsed["family"].(map[string]any); ok {
		if dep, ok := fam["deprecated"].(bool); ok && dep {
			status = "deprecated"
		}
		if life, ok := fam["lifecycle"].(map[string]any); ok {
			if s, ok := life["status"].(string); ok && s == "deprecated" {
				status = "deprecated"
			}
		}
	}
	labels["__gate:status"] = status

	return labels
}

// extractAtomVerb pulls a verb token from a free-form atom string. Heuristic:
// take the substring before '(' and trim spaces; failing that, take the first
// token. Used best-effort — empty return means "skip the label".
func extractAtomVerb(atom string) string {
	atom = strings.TrimSpace(atom)
	if atom == "" {
		return ""
	}
	if i := strings.Index(atom, "("); i > 0 {
		head := strings.TrimSpace(atom[:i])
		if head != "" {
			return head
		}
	}
	if fields := strings.Fields(atom); len(fields) > 0 {
		return fields[0]
	}
	return ""
}
