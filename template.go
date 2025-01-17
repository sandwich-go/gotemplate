// Reads the templates and writes the substituted templates

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

var testingMode = false

const (
	genHeader = "// Code generated by gotemplate. DO NOT EDIT.\n\n"
)

// Holds the desired template
type template struct {
	Package         string
	Name            string
	Args            []string
	NewPackage      string
	Dir             string
	templateName    string
	templateArgs    []string
	templateArgsMap map[string]string
	mappings        map[types.Object]string
	newIsPublic     bool
	inputFile       string
	formatFuncs     map[string]string
}

// findPackageName reads all the go packages in the curent directory
// and finds which package they are in
func findPackageName() string {
	p, err := build.Default.Import(".", ".", build.ImportMode(0))
	if err != nil {
		fatalf("Failed to read packages in current directory: %v", err)
	}
	return p.Name
}

// init the template instantiation
func newTemplate(dir, pkg, templateArgsString string) *template {
	name, templateArgs := parseTemplateAndArgs(templateArgsString)
	return &template{
		Package:         pkg,
		Name:            name,
		Args:            templateArgs,
		Dir:             dir,
		mappings:        make(map[types.Object]string),
		NewPackage:      findPackageName(),
		templateArgsMap: make(map[string]string),
		formatFuncs:     make(map[string]string),
	}
}

// Add a mapping for identifier
func (t *template) addMapping(object types.Object, name string) {
	replacementName := ""
	if !strings.Contains(name, t.templateName) {
		// If name doesn't contain template name then just prefix it
		innerName := strings.ToUpper(t.Name[:1]) + t.Name[1:]
		replacementName = name + innerName
		debugf("Top level definition '%s' doesn't contain template name '%s', using '%s'", name, t.templateName, replacementName)
	} else {
		// make sure the new identifier will follow
		// Go casing style (newMySet not newmySet).
		innerName := t.Name
		if strings.Index(name, t.templateName) != 0 {
			innerName = strings.ToUpper(innerName[:1]) + innerName[1:]
		}
		replacementName = strings.Replace(name, t.templateName, innerName, 1)
	}
	// If new template name is not public then make sure
	// the exported name is not public too
	if !t.newIsPublic && ast.IsExported(replacementName) {
		replacementName = strings.ToLower(replacementName[:1]) + replacementName[1:]
	}
	t.mappings[object] = replacementName
}

// Parse the arguments string Template(A, B, C)
func parseTemplateAndArgs(s string) (name string, args []string) {
	expr, err := parser.ParseExpr(s)
	if err != nil {
		fatalf("Failed to parse %q: %v", s, err)
	}
	debugf("expr = %#v\n", expr)
	callExpr, ok := expr.(*ast.CallExpr)
	if !ok {
		fatalf("Failed to parse %q: expecting Identifier(...)", s)
	}
	debugf("fun = %#v", callExpr.Fun)
	fn, ok := callExpr.Fun.(*ast.Ident)
	if !ok {
		fatalf("Failed to parse %q: expecting Identifier(...)", s)
	}
	name = fn.Name
	for i, arg := range callExpr.Args {
		var buf bytes.Buffer
		debugf("arg[%d] = %#v", i, arg)
		err = format.Node(&buf, token.NewFileSet(), arg)
		if err != nil {
			fatalf("Failed to format %q: %v", s, err)
		}
		s := buf.String()
		debugf("parsed = %q", s)
		args = append(args, s)
	}
	return
}

var (
	matchTemplateType = regexp.MustCompile(`^//\s*template\s+type\s+(\w+\s*.*?)\s*$`)
	matchFirstCap     = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap       = regexp.MustCompile("([a-z0-9])([A-Z])")
	matchFormat       = regexp.MustCompile(`^//\s*template\s+format\s*$`)
)

func snakeCase(str string) string {
	snake := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToLower(snake)
}

func (t *template) findTemplateDefinition(f *ast.File) {
	// Inspect the comments
	t.templateName = ""
	t.templateArgs = nil
	for _, cg := range f.Comments {
		for _, x := range cg.List {
			matches := matchTemplateType.FindStringSubmatch(x.Text)
			if matches != nil {
				if t.templateName != "" {
					fatalf("Found multiple template definitions in %s", t.inputFile)
				}
				t.templateName, t.templateArgs = parseTemplateAndArgs(matches[1])
			}
		}
	}
	if t.templateName == "" {
		fatalf("Didn't find template definition in %s", t.inputFile)
	}
	if len(t.templateArgs) != len(t.Args) {
		fatalf("Wrong number of arguments - template is expecting %d but %d supplied", len(t.Args), len(t.templateArgs))
	}
	for i, to := range t.Args {
		t.templateArgsMap[t.templateArgs[i]] = to
	}
	debugf("templateName = %v, templateArgs = %v", t.templateName, t.templateArgs)
}

// Parses a file into a Fileset and Ast
//
// Dies with a fatal error on error
func parseFile(path string, src interface{}) (*token.FileSet, *ast.File) {
	fset := token.NewFileSet() // positions are relative to fset
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		fatalf("Failed to parse file: %s", err)
	}
	return fset, f
}

// Replace the identifers in f
func replaceIdentifier(f *ast.File, info *types.Info, old types.Object, new string) {
	// We replace the identifier name with a string
	// which is a bit untidy if we weren't
	// replacing with an identifier
	for id, obj := range info.Defs {
		if obj == old {
			id.Name = new
		}
	}
	for id, obj := range info.Uses {
		if obj == old {
			id.Name = new
		} else {
			if var_, ok := obj.(*types.Var); ok && var_.Anonymous() {
				// This is an anonymous field in composite literal
				// We should replace it if we replace a type it represents
				if named, ok := var_.Type().(*types.Named); ok && named.Obj() == old {
					id.Name = new
				}
			}
		}
	}
}

// isTestDecl check ast.Decl is testing func by containing testing parameter
func isTestDecl(decl ast.Decl) (is bool) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Type == nil || d.Type.Params == nil {
			return
		}
		if len(d.Type.Params.List) == 0 {
			return
		}
		for _, l := range d.Type.Params.List {
			testingStar, ok0 := l.Type.(*ast.StarExpr)
			if !ok0 {
				return
			}
			selExpr, ok1 := testingStar.X.(*ast.SelectorExpr)
			if !ok1 {
				return
			}
			ident, ok2 := selExpr.X.(*ast.Ident)
			if !ok2 {
				return
			}
			is = ident.Name == "testing"
		}
	}
	return
}

func (t *template) rewriteFile(fset *token.FileSet, f *ast.File, outputFileName string, isTest bool) {
	b := new(bytes.Buffer)
	formatFunc := func() {
		b.Reset()
		if err := format.Node(b, fset, f); err != nil {
			fatalf("Failed to format output: %v", err)
		}
		bts, err := imports.Process(outputFileName, b.Bytes(), nil)
		if err != nil {
			fatalf("Cannot fix imports: %v", err)
		}
		b.Reset()
		if _, err := b.Write(bts); err != nil {
			fatalf("Cannot write output: %v", err)
		}
	}

	formatFunc()

	var ss = b.String()
	if !isTest && len(t.formatFuncs) > 0 {
		for k, v := range t.formatFuncs {
			ss = strings.ReplaceAll(ss, k, v)
		}
	}
	fset, f = parseFile(outputFileName, genHeader+ss)

	formatFunc()

	write := true

	var curr []byte
	if !testingMode {
		var err error
		curr, err = ioutil.ReadFile(outputFileName)
		if err != nil && !os.IsNotExist(err) {
			fatalf("Cannot open existing file: %v", err)
		}
	}

	if bytes.Equal(curr, b.Bytes()) {
		write = false
	}

	if write {
		err := ioutil.WriteFile(outputFileName, b.Bytes(), 0666)
		if err != nil {
			fatalf("Unable to write to %q: %v", outputFileName, err)
		}
	}

	debugf("Written '%s'", outputFileName)
}

var testingsMapping = map[string]struct{}{
	"\"testing\"": {},
	"\"github.com/smartystreets/goconvey/convey\"": {},
}

func isTestImport(imp *ast.ImportSpec) (is bool) {
	if imp == nil || imp.Path == nil {
		return
	}
	if _, ok := testingsMapping[imp.Path.Value]; ok {
		is = true
		return
	}
	return
}

func arrangeDecl(decl ast.Decl) (testDecl, genDecl ast.Decl) {
	genDecl = decl
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Type == nil || d.Type.Params == nil {
			return
		}
		if len(d.Type.Params.List) == 0 {
			return
		}
		for _, l := range d.Type.Params.List {
			testingStar, ok0 := l.Type.(*ast.StarExpr)
			if !ok0 {
				return
			}
			selExpr, ok1 := testingStar.X.(*ast.SelectorExpr)
			if !ok1 {
				return
			}
			ident, ok2 := selExpr.X.(*ast.Ident)
			if !ok2 {
				return
			}
			if ident.Name == "testing" {
				testDecl = decl
				genDecl = nil
			}
		}
	case *ast.GenDecl:
		switch d.Tok {
		case token.IMPORT:
			var is, tis []ast.Spec
			for _, im := range d.Specs {
				if isTestImport(im.(*ast.ImportSpec)) {
					tis = append(tis, im)
				} else {
					is = append(is, im)
				}
			}
			if len(tis) > 0 {
				cd := *d
				cd.Specs = tis
				d.Specs = is
				testDecl = &cd
				genDecl = d
			}
			return
		}
	}
	return
}

// Parses the template file
func (t *template) parse(inputFile string) {
	t.inputFile = inputFile
	// Make the name mappings
	t.newIsPublic = ast.IsExported(t.Name)

	conf := &packages.Config{
		Mode: packages.LoadSyntax,
	}

	pkgs, err := packages.Load(conf, inputFile)
	if err != nil {
		fatalf("Type checking error: %v", err)
	}

	pkg := pkgs[0]

	if len(pkg.Errors) > 0 {
		fatalf("Type checking error: %v", pkg.Errors[0])
	}

	info := pkg.TypesInfo
	fset := pkg.Fset
	f := pkg.Syntax[0]

	t.findTemplateDefinition(f)

	// debugf("Decls = %#v", f.Decls)
	// Find names which need to be adjusted
	namesToMangle := map[types.Object]string{}
	newDecls := []ast.Decl{}
	var hasTestingFunc bool
	for _, decl := range f.Decls {
		remove := false
		switch d := decl.(type) {
		case *ast.GenDecl:
			// A general definition
			switch d.Tok {
			case token.IMPORT:
				// Ignore imports
			case token.CONST, token.VAR:
				// Find and remove identifiers found in template
				// params
				emptySpecs := []int{}
				for i, spec := range d.Specs {
					namesToRemove := []int{}
					v := spec.(*ast.ValueSpec)
					for j, name := range v.Names {
						debugf("VAR or CONST %v", name.Name)
						def := info.Defs[name]
						if _, ok := t.templateArgsMap[name.Name]; ok {
							namesToRemove = append(namesToRemove, j)
							t.mappings[def] = t.templateArgsMap[name.Name]
						} else {
							namesToMangle[def] = name.Name
						}
					}
					// Shuffle the names to remove out of v.Names and v.Values
					for i := len(namesToRemove) - 1; i >= 0; i-- {
						p := namesToRemove[i]
						v.Names = append(v.Names[:p], v.Names[p+1:]...)
						v.Values = append(v.Values[:p], v.Values[p+1:]...)
					}
					// If empty then add to slice to remove later
					if len(v.Names) == 0 {
						emptySpecs = append(emptySpecs, i)
					}
				}
				// Remove now-empty specs
				for i := len(emptySpecs) - 1; i >= 0; i-- {
					p := emptySpecs[i]
					d.Specs = append(d.Specs[:p], d.Specs[p+1:]...)
				}
				remove = len(d.Specs) == 0
			case token.TYPE:
				namesToRemove := []int{}
				for i, spec := range d.Specs {
					typeSpec := spec.(*ast.TypeSpec)
					debugf("Type %v", typeSpec.Name.Name)
					// Remove type A if it is a template definition
					def := info.Defs[typeSpec.Name]
					if _, ok := t.templateArgsMap[typeSpec.Name.Name]; ok {
						namesToRemove = append(namesToRemove, i)
						t.mappings[def] = t.templateArgsMap[typeSpec.Name.Name]
					} else {
						namesToMangle[def] = typeSpec.Name.Name
					}
				}
				for i := len(namesToRemove) - 1; i >= 0; i-- {
					p := namesToRemove[i]
					d.Specs = append(d.Specs[:p], d.Specs[p+1:]...)
				}
				remove = len(d.Specs) == 0
			default:
				logf("Unknown type %s", d.Tok)
			}
			debugf("GenDecl = %#v", d)
		case *ast.FuncDecl:
			// A function definition
			if d.Recv != nil {
				// Has receiver so is a method - ignore this function
			} else if d.Name.Name == "init" {
				// Init function - ignore this function
			} else {
				if !hasTestingFunc && isTestDecl(d) {
					hasTestingFunc = true
				}
				//debugf("FuncDecl = %#v", d)
				debugf("FuncDecl = %s", d.Name.Name)
				def := info.Defs[d.Name]
				// Remove func A() if it is a template definition
				if _, ok := t.templateArgsMap[d.Name.Name]; ok {
					remove = true
					t.mappings[def] = t.templateArgsMap[d.Name.Name]
				} else {
					namesToMangle[def] = d.Name.Name
				}
			}
		default:
			fatalf("Unknown Decl %#v", decl)
		}
		if !remove {
			newDecls = append(newDecls, decl)
		}
	}
	debugf("Names to mangle = %#v", namesToMangle)

	// Remove the stub type definitions "type A int" from the package
	f.Decls = newDecls

	found := false
	for obj, name := range namesToMangle {
		if name == t.templateName {
			found = true
			t.addMapping(obj, name)
		} else if _, found := t.mappings[obj]; !found {
			t.addMapping(obj, name)
		}

	}
	if !found {
		fatalf("No definition for template type '%s'", t.templateName)
	}
	debugf("mappings = %#v", t.mappings)

	// Replace the identifiers
	for id, replacement := range t.mappings {
		replaceIdentifier(f, info, id, replacement)
	}

	// Change the package to the local package name
	f.Name.Name = t.NewPackage

	// Output but only if contents have changed from existing file

	var decls, testDecls []ast.Decl
	var testComments []*ast.CommentGroup
	var getComment = func(decl ast.Decl) {
		switch v := decl.(type) {
		case *ast.GenDecl:
			if v.Doc != nil {
				testComments = append(testComments, v.Doc)
			}
		case *ast.FuncDecl:
			if v.Doc != nil {
				testComments = append(testComments, v.Doc)
			}
		}
	}
	if hasTestingFunc {
		for _, decl := range f.Decls {
			testDecl, genDecl := arrangeDecl(decl)
			if testDecl != nil {
				testDecls = append(testDecls, testDecl)
				getComment(testDecl)
			}
			if genDecl != nil {
				t.reviseIfSpecialDecl(genDecl)
				decls = append(decls, genDecl)
			}
		}
		// remove testing function
		f.Decls = decls

		// remove test comments
		if len(testComments) > 0 {
			var comments = make([]*ast.CommentGroup, 0, len(f.Comments))
			for _, j := range f.Comments {
				var remove bool
				for _, td := range testComments {
					if j == td {
						remove = true
						break
					}
				}
				if !remove {
					comments = append(comments, j)
				}
			}
			if len(comments) != len(f.Comments) {
				f.Comments = comments
			}
		}
	}

	t.rewriteFile(fset, f, fmt.Sprintf(*outfile+".go", filename(t.Name)), false)

	if hasTestFile() && len(testDecls) > 0 {
		// remove other comments
		f.Comments = nil
		f.Decls = testDecls
		t.rewriteFile(fset, f, fmt.Sprintf(*outfile+"_test.go", filename(t.Name)), true)
	}
}

func (t *template) reviseIfSpecialDecl(decl ast.Decl) {
	switch v := decl.(type) {
	case *ast.GenDecl:
		if v.Doc == nil || len(v.Doc.List) == 0 {
			return
		}
		if len(v.Specs) == 0 || v.Specs[0] == nil {
			return
		}
		if _, ok := v.Specs[0].(*ast.ValueSpec); !ok || v.Specs[0].(*ast.ValueSpec).Type == nil {
			return
		}
		if _, ok := v.Specs[0].(*ast.ValueSpec).Type.(*ast.FuncType); !ok || v.Specs[0].(*ast.ValueSpec).Type.(*ast.FuncType).Results == nil {
			return
		}
		if len(v.Specs[0].(*ast.ValueSpec).Type.(*ast.FuncType).Results.List) == 0 ||
			v.Specs[0].(*ast.ValueSpec).Type.(*ast.FuncType).Results.List[0].Type == nil {
			return
		}
		if len(v.Specs) == 0 || len(v.Specs[0].(*ast.ValueSpec).Type.(*ast.FuncType).Results.List) == 0 {
			return
		}
		name := v.Specs[0].(*ast.ValueSpec).Type.(*ast.FuncType).Results.List[0].Type.(*ast.Ident).Name
		for _, cm := range v.Doc.List {
			if matches := matchFormat.FindStringSubmatch(cm.Text); len(matches) > 0 {
				if formatFunc := getFormatFunc(name); len(formatFunc) > 0 {
					b := new(bytes.Buffer)
					err := format.Node(b, token.NewFileSet(), v.Specs[0].(*ast.ValueSpec))
					if err != nil {
						fatalf("Format error for template type '%s', %v", t.templateName, err)
					}
					txt := b.String()
					t.formatFuncs[txt] = strings.Split(txt, " ")[0] + " = " + formatFunc
				}
				break
			}
		}
	}
}

// Instantiate the template package
func (t *template) instantiate() {
	debugf("Substituting %q with %s(%s) into package %s", t.Package, t.Name, strings.Join(t.Args, ","), t.NewPackage)

	p, err := build.Default.Import(t.Package, t.Dir, build.ImportMode(0))
	if err != nil {
		fatalf("Import %s failed: %s", t.Package, err)
	}
	//debugf("package = %#v", p)
	debugf("Dir = %#v", p.Dir)
	// FIXME CgoFiles ?
	debugf("Go files = %#v", p.GoFiles)

	if len(p.GoFiles) == 0 {
		fatalf("No go files found for package '%s'", t.Package)
	}
	// FIXME
	if len(p.GoFiles) != 1 {
		fatalf("Found more than one go file in '%s' - can only cope with 1 for the moment, sorry", t.Package)
	}
	for _, v := range p.GoFiles {
		templateFilePath := path.Join(p.Dir, v)
		t.parse(templateFilePath)
	}
}
