package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	template2 "html/template"
	"strings"
)

const formatTPL = `func(i interface{}) {{ .Type }} {
	switch ii := i.(type) {
{{- if eq .Type "string" }}
	case string:
		return ii
	default:
		return fmt.Sprintf("%d", i)
{{- else }}
	case int:
		return {{ .Type }}(ii)
	case int8:
		return {{ .Type }}(ii)
	case int16:
		return {{ .Type }}(ii)
	case int32:
		return {{ .Type }}(ii)
	case int64:
		return {{ .Type }}(ii)
	case uint:
		return {{ .Type }}(ii)
	case uint8:
		return {{ .Type }}(ii)
	case uint16:
		return {{ .Type }}(ii)
	case uint32:
		return {{ .Type }}(ii)
	case uint64:
		return {{ .Type }}(ii)
	case float32:
		return {{ .Type }}(ii)
	case float64:
		return {{ .Type }}(ii)
	case string:
	{{- if eq .Type "float32" }}
		iv, err := strconv.ParseFloat(ii, 32)
	{{- else if eq .Type "float64" }}
		iv, err := strconv.ParseFloat(ii, 64)
	{{- else if .Unsigned }}
		iv, err := strconv.ParseUint(ii, 10, 64)
	{{- else }}
		iv, err := strconv.ParseInt(ii, 10, 64)
	{{- end }}
		if err != nil {
			panic(err)
		}
		return {{ .Type }}(iv)
	default:
		panic("unknown type")
{{- end }}
	}
}`

var formatFuncs map[string]*ast.FuncLit

func getFormatFunc(t string) *ast.FuncLit {
	return formatFuncs[strings.ToLower(t)]
}

func init() {
	t, err := template2.New("format").Parse(formatTPL)
	if err != nil {
		panic(err)
	}
	formatFuncs = make(map[string]*ast.FuncLit)
	for _, v := range []struct {
		Type     string
		Unsigned bool
	}{
		{Type: "int"}, {Type: "int8"}, {Type: "int16"}, {Type: "int32"}, {Type: "int64"},
		{Type: "uint", Unsigned: true}, {Type: "uint8", Unsigned: true}, {Type: "uint16", Unsigned: true}, {Type: "uint32", Unsigned: true}, {Type: "uint64", Unsigned: true},
		{Type: "string"}, {Type: "float32"}, {Type: "float64"},
	} {
		buf := bytes.NewBuffer(nil)
		err = t.Execute(buf, v)
		if err != nil {
			panic(err)
		}
		f, err0 := parser.ParseExpr(buf.String())
		if err0 != nil {
			panic(err0)
		}
		formatFuncs[v.Type] = f.(*ast.FuncLit)
	}
}
