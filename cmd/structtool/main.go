package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	namespaceRe = regexp.MustCompile(`^// Namespace:\s*(.*)$`)
	classRe     = regexp.MustCompile(`^public\s+(?:sealed\s+|abstract\s+|static\s+|partial\s+)*(?:class|struct)\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	msgpackRe   = regexp.MustCompile(`^\s*\[MessagePackObject\(False\)\]\s*$`)
	keyAttrRe   = regexp.MustCompile(`^\s*\[Key\((.+)\)\]\s*$`)
	fieldRe     = regexp.MustCompile(`^\s*public\s+([^;()]+?)\s+([A-Za-z_][A-Za-z0-9_]*)\s*;\s*//`)
	arrayTypeRe = regexp.MustCompile(`^\s*(.+?)\s*\[\]\s*$`)
	listTypeRe  = regexp.MustCompile(`^\s*(?:List|IList|IReadOnlyList|ICollection|IEnumerable)<\s*([^>]+)\s*>\s*$`)
	nullableRe  = regexp.MustCompile(`^\s*Nullable<\s*([^>]+)\s*>\s*$`)
)

type DumpField struct {
	KeyKind string // int | str | ""
	KeyInt  int
	KeyStr  string
	Type    string
	Name    string
}

type DumpClass struct {
	Namespace        string
	Name             string
	MessagePackArray bool
	Fields           []DumpField
}

type DumpIndex struct {
	Classes  []*DumpClass
	byName   map[string][]*DumpClass
	byNSName map[string]map[string][]*DumpClass
}

type TypeRef struct {
	TypeName  string
	Namespace string
	Source    string
}

type Issue struct {
	Key     string
	Path    string
	Message string
}

type UserArrayTypeReport struct {
	Namespace   string           `json:"namespace"`
	Type        string           `json:"type"`
	IntKeyCount int              `json:"int_key_count"`
	MinKey      int              `json:"min_key"`
	MaxKey      int              `json:"max_key"`
	Fields      []UserArrayField `json:"fields"`
}

type UserArrayField struct {
	Key  int    `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type UserTypedFieldReport struct {
	Namespace string `json:"namespace"`
	OwnerType string `json:"owner_type"`
	Key       int    `json:"key"`
	Name      string `json:"name"`
	Type      string `json:"type"`
}

type UserExtractReport struct {
	Source  string `json:"source"`
	Summary struct {
		UserMsgpackArrayTypeCount int `json:"user_msgpack_array_type_count"`
		UserTypedIntKeyFieldCount int `json:"user_typed_intkey_field_count"`
	} `json:"summary"`
	UserMsgpackArrayTypes []UserArrayTypeReport  `json:"user_msgpack_array_types"`
	UserTypedIntKeyFields []UserTypedFieldReport `json:"user_typed_intkey_fields"`
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	var err error
	switch cmd {
	case "check":
		err = runCheck(os.Args[2:])
	case "update":
		err = runUpdate(os.Args[2:])
	case "extract-user":
		err = runExtractUser(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`structtool - dump.cs structure automation

Commands:
  check         Check a structures file against dump.cs type definitions
  update        Regenerate/replace structure keys from dump.cs
  extract-user  Extract User* msgpack-array report and suite structures

Examples:
  structtool check -dump dump.cs -structures structures.json -suite-class SuiteMaster
  structtool check -dump dump.cs -structures suite_structures.json -suite-class SuiteUser
  structtool update -dump dump.cs -structures structures.json -suite-class SuiteMaster -keys cards,events
  structtool extract-user -dump dump.cs -report-out user_msgpack_array_report.json -suite-out suite_structures.json`)
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	dumpPath := fs.String("dump", "dump.cs", "path to dump.cs")
	structuresPath := fs.String("structures", "structures.json", "path to structures json")
	suiteClass := fs.String("suite-class", "SuiteMaster", "container class name, e.g. SuiteMaster or SuiteUser")
	typePrefix := fs.String("type-prefix", "", "preferred type prefix, auto-detected when empty")
	maxIssues := fs.Int("max-issues", 200, "maximum issues to print (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	idx, err := parseDumpCS(*dumpPath)
	if err != nil {
		return err
	}
	structures, err := readJSONObject(*structuresPath)
	if err != nil {
		return err
	}

	prefix := *typePrefix
	if prefix == "" {
		prefix = defaultPrefixForSuite(*suiteClass)
	}

	refs, unresolved := mapTopLevelTypes(idx, mapKeys(structures), *suiteClass, prefix)
	issues := compareStructures(idx, structures, refs, unresolved)

	fmt.Printf("Checked %d keys in %s\n", len(structures), filepath.Base(*structuresPath))
	fmt.Printf("Mapped: %d, Unresolved: %d, Issues: %d\n", len(refs), len(unresolved), len(issues))

	if len(issues) > 0 {
		limit := len(issues)
		if *maxIssues > 0 && *maxIssues < limit {
			limit = *maxIssues
		}
		for i := 0; i < limit; i++ {
			iss := issues[i]
			fmt.Printf("- [%s] %s: %s\n", iss.Key, iss.Path, iss.Message)
		}
		if limit < len(issues) {
			fmt.Printf("... %d more issues omitted\n", len(issues)-limit)
		}
		return errors.New("structure check failed")
	}

	fmt.Println("OK")
	return nil
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	dumpPath := fs.String("dump", "dump.cs", "path to dump.cs")
	structuresPath := fs.String("structures", "structures.json", "input structures json")
	outPath := fs.String("out", "", "output file path (default: overwrite -structures)")
	suiteClass := fs.String("suite-class", "SuiteMaster", "container class name, e.g. SuiteMaster or SuiteUser")
	typePrefix := fs.String("type-prefix", "", "preferred type prefix, auto-detected when empty")
	keysArg := fs.String("keys", "", "comma-separated keys to update; empty updates existing keys")
	addMissing := fs.Bool("add-missing", false, "when -keys is empty, also add mappable keys that are missing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	idx, err := parseDumpCS(*dumpPath)
	if err != nil {
		return err
	}
	structures, err := readJSONObject(*structuresPath)
	if err != nil {
		return err
	}

	prefix := *typePrefix
	if prefix == "" {
		prefix = defaultPrefixForSuite(*suiteClass)
	}

	var targetKeys []string
	if strings.TrimSpace(*keysArg) != "" {
		targetKeys = parseCSV(*keysArg)
	} else {
		targetKeys = mapKeys(structures)
		sort.Strings(targetKeys)
	}

	refs, _ := mapTopLevelTypes(idx, uniqueStrings(targetKeys), *suiteClass, prefix)

	if strings.TrimSpace(*keysArg) == "" && *addMissing {
		allKeys := mapKeys(structures)
		refsAll, _ := mapTopLevelTypes(idx, allKeys, *suiteClass, prefix)
		for k, v := range refsAll {
			refs[k] = v
		}
	}

	updated := 0
	skipped := 0
	for _, key := range uniqueStrings(targetKeys) {
		ref, ok := refs[key]
		if !ok {
			skipped++
			fmt.Fprintf(os.Stderr, "warn: key %q has no mapped type\n", key)
			continue
		}
		schema, err := buildSchemaForType(idx, ref.TypeName, ref.Namespace, make(map[string]bool))
		if err != nil {
			skipped++
			fmt.Fprintf(os.Stderr, "warn: key %q build failed: %v\n", key, err)
			continue
		}
		structures[key] = schema
		updated++
	}

	if strings.TrimSpace(*keysArg) == "" && *addMissing {
		allRefs, _ := mapTopLevelTypes(idx, nil, *suiteClass, prefix)
		for k, ref := range allRefs {
			if _, exists := structures[k]; exists {
				continue
			}
			schema, err := buildSchemaForType(idx, ref.TypeName, ref.Namespace, make(map[string]bool))
			if err != nil {
				continue
			}
			structures[k] = schema
			updated++
		}
	}

	outputPath := *outPath
	if outputPath == "" {
		outputPath = *structuresPath
	}
	if err := writeJSON(outputPath, structures); err != nil {
		return err
	}

	fmt.Printf("Updated %d keys, skipped %d, wrote %s\n", updated, skipped, outputPath)
	return nil
}

func runExtractUser(args []string) error {
	fs := flag.NewFlagSet("extract-user", flag.ContinueOnError)
	dumpPath := fs.String("dump", "dump.cs", "path to dump.cs")
	reportOut := fs.String("report-out", "user_msgpack_array_report.json", "output report json")
	suiteOut := fs.String("suite-out", "suite_structures.json", "output suite structures json")
	suiteClass := fs.String("suite-class", "SuiteUser", "suite class for top-level key mapping")
	if err := fs.Parse(args); err != nil {
		return err
	}

	idx, err := parseDumpCS(*dumpPath)
	if err != nil {
		return err
	}

	report := extractUserReport(idx)
	if err := writeJSON(*reportOut, report); err != nil {
		return err
	}

	userTypes := make(map[string]UserArrayTypeReport, len(report.UserMsgpackArrayTypes))
	for _, it := range report.UserMsgpackArrayTypes {
		userTypes[it.Type] = it
	}

	suiteMap, err := buildSuiteUserStructures(idx, *suiteClass, userTypes)
	if err != nil {
		return err
	}
	if err := writeJSON(*suiteOut, suiteMap); err != nil {
		return err
	}

	fmt.Printf("Extracted %d User* msgpack-array types\n", report.Summary.UserMsgpackArrayTypeCount)
	fmt.Printf("Wrote %s and %s\n", *reportOut, *suiteOut)
	return nil
}

func parseDumpCS(path string) (*DumpIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	if err := s.Err(); err != nil {
		return nil, err
	}

	classes := make([]*DumpClass, 0, 4096)
	currentNS := ""

	for i := 0; i < len(lines); {
		if m := namespaceRe.FindStringSubmatch(lines[i]); m != nil {
			currentNS = strings.TrimSpace(m[1])
			i++
			continue
		}

		attrs := make([]string, 0, 4)
		j := i
		for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
			attrs = append(attrs, strings.TrimSpace(lines[j]))
			j++
		}

		if j >= len(lines) {
			break
		}
		mClass := classRe.FindStringSubmatch(strings.TrimSpace(lines[j]))
		if mClass == nil {
			i++
			continue
		}

		name := mClass[1]
		msgpackArray := false
		for _, a := range attrs {
			if msgpackRe.MatchString(a) {
				msgpackArray = true
				break
			}
		}

		i = j
		depth := strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
		i++
		for i < len(lines) && depth <= 0 {
			depth += strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			i++
		}

		fields := make([]DumpField, 0, 64)
		pendingKind := ""
		pendingInt := 0
		pendingStr := ""

		for i < len(lines) && depth > 0 {
			line := lines[i]
			if mk := keyAttrRe.FindStringSubmatch(line); mk != nil {
				raw := strings.TrimSpace(mk[1])
				pendingKind, pendingInt, pendingStr = "", 0, ""
				if v, err := strconv.Atoi(raw); err == nil {
					pendingKind = "int"
					pendingInt = v
				} else {
					if sm := regexp.MustCompile(`^"([^"]+)"$`).FindStringSubmatch(raw); sm != nil {
						pendingKind = "str"
						pendingStr = sm[1]
					}
				}
			} else {
				if mf := fieldRe.FindStringSubmatch(line); mf != nil && pendingKind != "" {
					fields = append(fields, DumpField{
						KeyKind: pendingKind,
						KeyInt:  pendingInt,
						KeyStr:  pendingStr,
						Type:    strings.TrimSpace(mf[1]),
						Name:    strings.TrimSpace(mf[2]),
					})
					pendingKind, pendingInt, pendingStr = "", 0, ""
				}
			}

			depth += strings.Count(line, "{") - strings.Count(line, "}")
			i++
		}

		classes = append(classes, &DumpClass{
			Namespace:        currentNS,
			Name:             name,
			MessagePackArray: msgpackArray,
			Fields:           fields,
		})
	}

	idx := &DumpIndex{
		Classes:  classes,
		byName:   make(map[string][]*DumpClass),
		byNSName: make(map[string]map[string][]*DumpClass),
	}
	for _, c := range classes {
		idx.byName[c.Name] = append(idx.byName[c.Name], c)
		if _, ok := idx.byNSName[c.Namespace]; !ok {
			idx.byNSName[c.Namespace] = make(map[string][]*DumpClass)
		}
		idx.byNSName[c.Namespace][c.Name] = append(idx.byNSName[c.Namespace][c.Name], c)
	}
	return idx, nil
}

func classScore(c *DumpClass) int {
	intKeys := 0
	for _, f := range c.Fields {
		if f.KeyKind == "int" {
			intKeys++
		}
	}
	return intKeys*10000 + len(c.Fields)
}

func (idx *DumpIndex) chooseClass(name, preferNS string, requireInt bool) *DumpClass {
	var cands []*DumpClass
	if preferNS != "" {
		if nsMap, ok := idx.byNSName[preferNS]; ok {
			if xs := nsMap[name]; len(xs) > 0 {
				cands = append(cands, xs...)
			}
		}
	}
	if len(cands) == 0 {
		cands = idx.byName[name]
	}
	if len(cands) == 0 {
		return nil
	}

	best := (*DumpClass)(nil)
	bestScore := -1
	for _, c := range cands {
		s := classScore(c)
		if requireInt && countIntKeyFields(c) == 0 {
			continue
		}
		if s > bestScore {
			bestScore = s
			best = c
		}
	}
	return best
}

func countIntKeyFields(c *DumpClass) int {
	n := 0
	for _, f := range c.Fields {
		if f.KeyKind == "int" {
			n++
		}
	}
	return n
}

func intFieldMaps(c *DumpClass) (map[int]DumpField, map[string]DumpField) {
	byIdx := make(map[int]DumpField)
	byName := make(map[string]DumpField)
	for _, f := range c.Fields {
		if f.KeyKind != "int" {
			continue
		}
		byIdx[f.KeyInt] = f
		byName[strings.ToLower(f.Name)] = f
	}
	return byIdx, byName
}

func mapTopLevelTypes(idx *DumpIndex, keys []string, suiteClass string, typePrefix string) (map[string]TypeRef, map[string]bool) {
	if len(keys) == 0 {
		keys = nil
	}
	keySet := make(map[string]bool)
	if keys != nil {
		for _, k := range keys {
			keySet[k] = true
		}
	}

	type cand struct {
		key     string
		ref     TypeRef
		score   int
		typeCls *DumpClass
	}
	best := make(map[string]cand)

	consider := func(owner *DumpClass, f DumpField, bonus int) {
		if f.KeyKind != "str" {
			return
		}
		if keys != nil && !keySet[f.KeyStr] {
			return
		}
		elem, ok := collectionElemType(f.Type)
		if !ok {
			return
		}

		resolved := idx.chooseClass(elem, owner.Namespace, true)
		refNS := owner.Namespace
		if resolved != nil {
			refNS = resolved.Namespace
		}

		score := bonus
		if resolved != nil {
			score += classScore(resolved)
			if resolved.MessagePackArray {
				score += 500
			}
		}
		if typePrefix != "" && strings.HasPrefix(elem, typePrefix) {
			score += 5000
		}

		c := cand{
			key:     f.KeyStr,
			ref:     TypeRef{TypeName: elem, Namespace: refNS, Source: owner.Name + "." + f.Name},
			score:   score,
			typeCls: resolved,
		}
		if prev, ok := best[f.KeyStr]; !ok || c.score > prev.score {
			best[f.KeyStr] = c
		}
	}

	if suite := idx.chooseClass(suiteClass, "Sekai", false); suite != nil {
		for _, f := range suite.Fields {
			consider(suite, f, 100000)
		}
	}

	for _, owner := range idx.Classes {
		for _, f := range owner.Fields {
			consider(owner, f, 0)
		}
	}

	refs := make(map[string]TypeRef)
	for k, c := range best {
		refs[k] = c.ref
	}
	unresolved := make(map[string]bool)
	if keys != nil {
		for _, k := range keys {
			if _, ok := refs[k]; !ok {
				unresolved[k] = true
			}
		}
	}

	return refs, unresolved
}

func compareStructures(idx *DumpIndex, structures map[string]any, refs map[string]TypeRef, unresolved map[string]bool) []Issue {
	issues := make([]Issue, 0)
	for k := range unresolved {
		issues = append(issues, Issue{
			Key:     k,
			Path:    k,
			Message: "cannot map key to top-level type",
		})
	}

	keys := mapKeys(structures)
	sort.Strings(keys)
	for _, key := range keys {
		raw := structures[key]
		arr, ok := raw.([]any)
		if !ok {
			issues = append(issues, Issue{Key: key, Path: key, Message: "root is not an array schema"})
			continue
		}
		ref, ok := refs[key]
		if !ok {
			continue
		}
		compareSchemaWithType(idx, key, key, arr, ref.TypeName, ref.Namespace, &issues, make(map[string]bool))
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Key == issues[j].Key {
			return issues[i].Path < issues[j].Path
		}
		return issues[i].Key < issues[j].Key
	})
	return issues
}

func compareSchemaWithType(idx *DumpIndex, rootKey, path string, schema []any, typeName, ns string, issues *[]Issue, visiting map[string]bool) {
	visitKey := ns + "::" + typeName
	if visiting[visitKey] {
		return
	}
	visiting[visitKey] = true
	defer delete(visiting, visitKey)

	cls := idx.chooseClass(typeName, ns, true)
	if cls == nil {
		*issues = append(*issues, Issue{Key: rootKey, Path: path, Message: fmt.Sprintf("class %q not found or has no int [Key] fields", typeName)})
		return
	}

	byIdx, _ := intFieldMaps(cls)
	maxSchemaIdx := len(schema) - 1

	for i, entry := range schema {
		name, nested, placeholder, ok := parseSchemaEntry(entry)
		if !ok {
			*issues = append(*issues, Issue{Key: rootKey, Path: fmt.Sprintf("%s[%d]", path, i), Message: "invalid schema entry"})
			continue
		}
		if placeholder {
			continue
		}

		field, exists := byIdx[i]
		if !exists {
			*issues = append(*issues, Issue{Key: rootKey, Path: fmt.Sprintf("%s[%d]", path, i), Message: fmt.Sprintf("schema has %q at [Key(%d)] but type %q has no field", name, i, cls.Name)})
			continue
		}
		if !strings.EqualFold(name, field.Name) {
			*issues = append(*issues, Issue{Key: rootKey, Path: fmt.Sprintf("%s[%d]", path, i), Message: fmt.Sprintf("name mismatch: schema %q vs type %q", name, field.Name)})
		}

		if nested == nil {
			continue
		}

		childType, childIsCollection := fieldChildType(field.Type)
		if childType == "" {
			continue
		}

		if nestedArr, ok := nested.([]any); ok {
			compareSchemaWithType(idx, rootKey, path+"."+name, nestedArr, childType, cls.Namespace, issues, visiting)
			continue
		}

		if nestedMap, ok := nested.(map[string]any); ok {
			tupleRaw, ok := nestedMap["__tuple__"]
			if !ok {
				*issues = append(*issues, Issue{Key: rootKey, Path: path + "." + name, Message: "nested object must contain __tuple__"})
				continue
			}
			tupleArr, ok := tupleRaw.([]any)
			if !ok {
				*issues = append(*issues, Issue{Key: rootKey, Path: path + "." + name, Message: "__tuple__ must be an array"})
				continue
			}
			compareSchemaWithType(idx, rootKey, path+"."+name, tupleArr, childType, cls.Namespace, issues, visiting)
			continue
		}

		if !childIsCollection {
			*issues = append(*issues, Issue{Key: rootKey, Path: path + "." + name, Message: "nested scalar/object schema is invalid"})
		}
	}

	for key, field := range byIdx {
		if key > maxSchemaIdx {
			*issues = append(*issues, Issue{Key: rootKey, Path: path, Message: fmt.Sprintf("type %q has extra [Key(%d)] field %q", cls.Name, key, field.Name)})
		}
	}
}

func parseSchemaEntry(v any) (name string, nested any, placeholder bool, ok bool) {
	if v == nil {
		return "", nil, true, true
	}
	if s, ok := v.(string); ok {
		return s, nil, false, true
	}
	arr, ok := v.([]any)
	if !ok || len(arr) < 2 {
		return "", nil, false, false
	}
	name, ok = arr[0].(string)
	if !ok {
		return "", nil, false, false
	}
	return name, arr[1], false, true
}

func fieldChildType(fieldType string) (typeName string, isCollection bool) {
	if elem, ok := collectionElemType(fieldType); ok {
		return unwrapNullable(elem), true
	}
	return unwrapNullable(fieldType), false
}

func buildSchemaForType(idx *DumpIndex, typeName, ns string, visiting map[string]bool) ([]any, error) {
	visitKey := ns + "::" + typeName
	if visiting[visitKey] {
		return []any{}, nil
	}
	visiting[visitKey] = true
	defer delete(visiting, visitKey)

	cls := idx.chooseClass(typeName, ns, true)
	if cls == nil {
		return nil, fmt.Errorf("type %q not found or has no int-key fields", typeName)
	}

	byIdx, _ := intFieldMaps(cls)
	if len(byIdx) == 0 {
		return []any{}, nil
	}

	maxKey := -1
	for k := range byIdx {
		if k > maxKey {
			maxKey = k
		}
	}
	if maxKey < 0 {
		return []any{}, nil
	}

	schema := make([]any, maxKey+1)
	for i := 0; i <= maxKey; i++ {
		f, ok := byIdx[i]
		if !ok {
			schema[i] = nil
			continue
		}

		if elem, ok := collectionElemType(f.Type); ok {
			elem = unwrapNullable(elem)
			child := idx.chooseClass(elem, cls.Namespace, true)
			if child != nil {
				childSchema, err := buildSchemaForType(idx, elem, cls.Namespace, visiting)
				if err != nil {
					return nil, err
				}
				schema[i] = []any{f.Name, childSchema}
			} else {
				schema[i] = f.Name
			}
			continue
		}

		base := unwrapNullable(f.Type)
		child := idx.chooseClass(base, cls.Namespace, true)
		if child != nil {
			childSchema, err := buildSchemaForType(idx, base, cls.Namespace, visiting)
			if err != nil {
				return nil, err
			}
			schema[i] = []any{f.Name, map[string]any{"__tuple__": childSchema}}
		} else {
			schema[i] = f.Name
		}
	}

	for len(schema) > 0 && schema[len(schema)-1] == nil {
		schema = schema[:len(schema)-1]
	}
	return schema, nil
}

func extractUserReport(idx *DumpIndex) UserExtractReport {
	report := UserExtractReport{Source: "dump.cs"}

	userTypes := make([]UserArrayTypeReport, 0)
	for _, c := range idx.Classes {
		if !strings.HasPrefix(c.Name, "User") || !c.MessagePackArray {
			continue
		}
		intFields := make([]DumpField, 0)
		for _, f := range c.Fields {
			if f.KeyKind == "int" {
				intFields = append(intFields, f)
			}
		}
		if len(intFields) == 0 {
			continue
		}
		sort.Slice(intFields, func(i, j int) bool { return intFields[i].KeyInt < intFields[j].KeyInt })

		item := UserArrayTypeReport{
			Namespace:   c.Namespace,
			Type:        c.Name,
			IntKeyCount: len(intFields),
			MinKey:      intFields[0].KeyInt,
			MaxKey:      intFields[len(intFields)-1].KeyInt,
			Fields:      make([]UserArrayField, 0, len(intFields)),
		}
		for _, f := range intFields {
			item.Fields = append(item.Fields, UserArrayField{Key: f.KeyInt, Name: f.Name, Type: f.Type})
		}
		userTypes = append(userTypes, item)
	}
	sort.Slice(userTypes, func(i, j int) bool {
		if userTypes[i].Type == userTypes[j].Type {
			return userTypes[i].Namespace < userTypes[j].Namespace
		}
		return userTypes[i].Type < userTypes[j].Type
	})

	userTypeRe := regexp.MustCompile(`\bUser[A-Za-z0-9_\.]*\b`)
	userTypedFields := make([]UserTypedFieldReport, 0)
	for _, c := range idx.Classes {
		if !c.MessagePackArray {
			continue
		}
		for _, f := range c.Fields {
			if f.KeyKind != "int" {
				continue
			}
			if userTypeRe.MatchString(f.Type) {
				userTypedFields = append(userTypedFields, UserTypedFieldReport{
					Namespace: c.Namespace,
					OwnerType: c.Name,
					Key:       f.KeyInt,
					Name:      f.Name,
					Type:      f.Type,
				})
			}
		}
	}
	sort.Slice(userTypedFields, func(i, j int) bool {
		if userTypedFields[i].OwnerType == userTypedFields[j].OwnerType {
			if userTypedFields[i].Key == userTypedFields[j].Key {
				return userTypedFields[i].Name < userTypedFields[j].Name
			}
			return userTypedFields[i].Key < userTypedFields[j].Key
		}
		return userTypedFields[i].OwnerType < userTypedFields[j].OwnerType
	})

	report.Summary.UserMsgpackArrayTypeCount = len(userTypes)
	report.Summary.UserTypedIntKeyFieldCount = len(userTypedFields)
	report.UserMsgpackArrayTypes = userTypes
	report.UserTypedIntKeyFields = userTypedFields
	return report
}

func buildSuiteUserStructures(idx *DumpIndex, suiteClass string, userTypes map[string]UserArrayTypeReport) (map[string]any, error) {
	suite := idx.chooseClass(suiteClass, "Sekai", false)
	if suite == nil {
		return nil, fmt.Errorf("suite class %q not found", suiteClass)
	}

	result := make(map[string]any)
	for _, f := range suite.Fields {
		if f.KeyKind != "str" {
			continue
		}
		elem, ok := collectionElemType(f.Type)
		if !ok {
			continue
		}
		if _, ok := userTypes[elem]; !ok {
			continue
		}
		schema, err := buildSchemaForType(idx, elem, suite.Namespace, make(map[string]bool))
		if err != nil {
			return nil, err
		}
		result[f.KeyStr] = schema
	}
	return result, nil
}

func readJSONObject(path string) (map[string]any, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(buf, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		obj = make(map[string]any)
	}
	return obj, nil
}

func writeJSON(path string, value any) error {
	buf, err := json.MarshalIndent(value, "", "    ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return os.WriteFile(path, buf, 0o644)
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func parseCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func uniqueStrings(xs []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

func defaultPrefixForSuite(suiteClass string) string {
	lower := strings.ToLower(suiteClass)
	switch {
	case strings.Contains(lower, "user"):
		return "User"
	case strings.Contains(lower, "master"):
		return "Master"
	default:
		return ""
	}
}

func collectionElemType(t string) (string, bool) {
	if m := arrayTypeRe.FindStringSubmatch(strings.TrimSpace(t)); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	if m := listTypeRe.FindStringSubmatch(strings.TrimSpace(t)); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	return "", false
}

func unwrapNullable(t string) string {
	t = strings.TrimSpace(t)
	if m := nullableRe.FindStringSubmatch(t); m != nil {
		return strings.TrimSpace(m[1])
	}
	return t
}
