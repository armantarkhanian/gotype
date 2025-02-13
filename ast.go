package gotype

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type sourceFinder interface {
	GetPackageSourceFiles(packagePath string) ([]string, error)
}

type astTypeGenerator struct {
	sourceFinder sourceFinder
}

func (f *astTypeGenerator) GenerateTypesFromSpecs(typeSpecs ...TypeSpec) ([]Type, error) {
	packagePathToSpecs := f.groupTypeSpecByPackage(typeSpecs)

	resultMap := make(map[TypeSpec]Type)
	for packagePath, specs := range packagePathToSpecs {
		types, err := f.generateTypesInSinglePackage(packagePath, specs...)
		if err != nil {
			return nil, err
		}

		for i, typ := range types {
			resultMap[TypeSpec{PackagePath: packagePath, Name: specs[i]}] = typ
		}
	}

	results := make([]Type, 0, len(typeSpecs))
	for _, spec := range typeSpecs {
		results = append(results, resultMap[spec])
	}
	return results, nil
}

func (f *astTypeGenerator) groupTypeSpecByPackage(typeSpecs []TypeSpec) map[string][]string {
	result := make(map[string][]string)
	for _, spec := range typeSpecs {
		result[spec.PackagePath] = append(result[spec.PackagePath], spec.Name)
	}
	return result
}

func (f *astTypeGenerator) generateTypesInSinglePackage(packagePath string, names ...string) ([]Type, error) {
	goSources, err := f.sourceFinder.GetPackageSourceFiles(packagePath)
	if err != nil {
		return nil, err
	}

	remainingNames := make(map[string]struct{})
	for _, name := range names {
		remainingNames[name] = struct{}{}
	}

	resultMap := make(map[string]Type)
	for _, source := range goSources {
		if len(remainingNames) == 0 {
			break
		}

		fileAst, err := f.parseAstFile(source)
		if err != nil {
			return nil, err
		}

		importMap := f.generateImportMap(packagePath, fileAst)

		for name := range remainingNames {
			spec := f.getDeclarationByName(fileAst, name)
			if spec != nil {
				resultMap[name], err = f.generateTypeFromExpr(spec.Type, packagePath, importMap)
				if err != nil {
					return nil, err
				}
				delete(remainingNames, name)
			}
		}
	}

	if len(remainingNames) != 0 {
		// TODO (jauhararifin): give better error message
		for name := range remainingNames {
			return nil, fmt.Errorf("cannot find definition of %s. Probably you should organize your go.mod file and impots", name)
		}
	}

	results := make([]Type, 0, len(names))
	for _, name := range names {
		results = append(results, resultMap[name])
	}

	return results, nil
}

func (f *astTypeGenerator) parseAstFile(filename string) (*ast.File, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("cannot open file: %w", err)
	}
	defer file.Close()

	fset := token.NewFileSet()
	fileAst, err := parser.ParseFile(fset, filepath.Base(filename), file, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot parse go code: %w", err)
	}

	return fileAst, nil
}

func (f *astTypeGenerator) generateImportMap(packagePath string, file *ast.File) map[string]string {
	importMap := make(map[string]string)
	for _, decl := range file.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range genDecl.Specs {
				if importSpec, ok := spec.(*ast.ImportSpec); ok {
					importPath := importSpec.Path.Value
					importPath = strings.TrimPrefix(strings.TrimSuffix(importPath, "\""), "\"")

					importName := f.getImportNameFromPackagePath(importPath)
					if importSpec.Name != nil {
						importName = importSpec.Name.String()
					}

					importMap[importName] = importPath
				}
			}
		}
	}

	if !strings.Contains(file.Name.Name, "_test") {
		importMap[packagePath+"__short"] = file.Name.Name
	}
	return importMap
}

func (f *astTypeGenerator) getImportNameFromPackagePath(packagePath string) string {
	// TODO (jauhararifin): check the correctness of this
	base := path.Base(packagePath)
	if base == "" {
		return ""
	}
	return strings.Split(base, ".")[0]
}

func (*astTypeGenerator) getDeclarationByName(f *ast.File, name string) *ast.TypeSpec {
	for _, decl := range f.Decls {
		if genDecl, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range genDecl.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.String() == name {
					return typeSpec
				}
			}
		}
	}
	return nil
}

func (f *astTypeGenerator) generateTypeFromExpr(
	e ast.Expr,
	targetPkgPath string,
	importMap map[string]string,
) (Type, error) {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return f.generateTypeFromSelectorExpr(v, importMap)
	case *ast.Ident:
		return f.generateTypeFromIdent(v, targetPkgPath, importMap), nil
	case *ast.StarExpr:
		typ, err := f.generateTypeFromStarExpr(v, targetPkgPath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{PtrType: &typ}, nil
	case *ast.ArrayType:
		return f.generateTypeFromArrayType(v, targetPkgPath, importMap)
	case *ast.FuncType:
		typ, err := f.generateTypeFromFuncType(v, targetPkgPath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{FuncType: &typ}, nil
	case *ast.MapType:
		typ, err := f.generateTypeFromMapType(v, targetPkgPath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{MapType: &typ}, nil
	case *ast.ChanType:
		typ, err := f.generateTypeFromChanType(v, targetPkgPath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{ChanType: &typ}, nil
	case *ast.StructType:
		typ, err := f.generateTypeFromStructType(v, targetPkgPath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{StructType: &typ}, nil
	case *ast.InterfaceType:
		typ, err := f.generateTypeFromInterfaceType(v, targetPkgPath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{InterfaceType: &typ}, nil
	}
	return Type{}, fmt.Errorf("unrecognized type: %v", e)
}

func (f *astTypeGenerator) generateTypeFromIdent(ident *ast.Ident, packagePath string, importMap map[string]string) Type {
	switch ident.Name {
	case string(PrimitiveKindBool):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindBool}}
	case string(PrimitiveKindByte):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindByte}}
	case string(PrimitiveKindRune):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindRune}}
	case string(PrimitiveKindInt):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindInt}}
	case string(PrimitiveKindInt8):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindInt8}}
	case string(PrimitiveKindInt16):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindInt16}}
	case string(PrimitiveKindInt32):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindInt32}}
	case string(PrimitiveKindInt64):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindInt64}}
	case string(PrimitiveKindUint):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindUint}}
	case string(PrimitiveKindUint8):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindUint8}}
	case string(PrimitiveKindUint16):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindUint16}}
	case string(PrimitiveKindUint32):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindUint32}}
	case string(PrimitiveKindUint64):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindUint64}}
	case string(PrimitiveKindUintptr):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindUintptr}}
	case string(PrimitiveKindFloat32):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindFloat32}}
	case string(PrimitiveKindFloat64):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindFloat64}}
	case string(PrimitiveKindComplex64):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindComplex64}}
	case string(PrimitiveKindComplex128):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindComplex128}}
	case string(PrimitiveKindString):
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindString}}
	case "error":
		return Type{PrimitiveType: &PrimitiveType{Kind: PrimitiveKindError}}
	}

	// Так и не понял, почему заходим сюда, но на некоторых импоратх, мы сюда заходим и это все ломает
	// Покопался в коде, сделал костыль, но надо будет потом все сделать по уму
	return Type{QualType: &QualType{
		Package:          packagePath,
		ShortPackagePath: importMap[packagePath+"__short"],
		Name:             ident.Name,
	}}
}

func (f *astTypeGenerator) generateTypeFromSelectorExpr(
	selectorExpr *ast.SelectorExpr,
	importMap map[string]string,
) (Type, error) {
	ident, ok := selectorExpr.X.(*ast.Ident)
	if !ok {
		return Type{}, fmt.Errorf("unrecognized expr: %v", selectorExpr)
	}

	shortImport := ident.String()

	importPath, ok := importMap[shortImport]
	if !ok {
		return Type{}, fmt.Errorf("unrecognized identifier: %s", shortImport)
	}

	return Type{QualType: &QualType{
		Package:          importPath,
		ShortPackagePath: shortImport,
		Name:             selectorExpr.Sel.String(),
	}}, nil
}

func (f *astTypeGenerator) generateTypeFromStarExpr(
	starExpr *ast.StarExpr,
	packagePath string,
	importMap map[string]string,
) (PtrType, error) {
	elem, err := f.generateTypeFromExpr(starExpr.X, packagePath, importMap)
	if err != nil {
		return PtrType{}, err
	}
	return PtrType{Elem: elem}, nil
}

func (f *astTypeGenerator) generateTypeFromArrayType(
	arrayType *ast.ArrayType,
	packagePath string,
	importMap map[string]string,
) (Type, error) {
	if arrayType.Len == nil {
		elem, err := f.generateTypeFromExpr(arrayType.Elt, packagePath, importMap)
		if err != nil {
			return Type{}, err
		}
		return Type{SliceType: &SliceType{Elem: elem}}, nil
	}

	lit, ok := arrayType.Len.(*ast.BasicLit)
	if !ok {
		return Type{}, fmt.Errorf("unrecognized array length: %v", arrayType.Len)
	}
	lenn, ok := parseInt(lit.Value)
	if !ok {
		return Type{}, fmt.Errorf("unrecognized array length: %v", lit.Value)
	}

	elem, err := f.generateTypeFromExpr(arrayType.Elt, packagePath, importMap)
	if err != nil {
		return Type{}, err
	}

	return Type{ArrayType: &ArrayType{Len: lenn, Elem: elem}}, nil
}

func (f *astTypeGenerator) generateTypeFromFuncType(
	funcType *ast.FuncType,
	packagePath string,
	importMap map[string]string,
) (FuncType, error) {
	params, isVariadic, err := f.generateTypeFromFieldList(
		funcType.Params,
		f.getInputNamesFromAst(funcType.Params.List),
		packagePath,
		importMap,
	)
	if err != nil {
		return FuncType{}, err
	}

	var results []TypeField = nil
	if funcType.Results != nil {
		if results, _, err = f.generateTypeFromFieldList(
			funcType.Results,
			f.getOutputNamesFromAst(funcType.Results.List),
			packagePath,
			importMap,
		); err != nil {
			return FuncType{}, err
		}
	}

	return FuncType{
		Inputs:     params,
		Outputs:    results,
		IsVariadic: isVariadic,
	}, nil
}

func (f *astTypeGenerator) generateTypeFromFieldList(
	fields *ast.FieldList,
	names []string,
	packagePath string,
	importMap map[string]string,
) (types []TypeField, isVariadic bool, err error) {
	if fields == nil {
		return nil, false, nil
	}

	types = make([]TypeField, 0, fields.NumFields())
	i := 0
	for _, field := range fields.List {
		typExpr := field.Type
		if v, ok := field.Type.(*ast.Ellipsis); ok {
			isVariadic = true
			typExpr = v.Elt
		}

		typ, err := f.generateTypeFromExpr(typExpr, packagePath, importMap)
		if err != nil {
			return nil, false, err
		}

		if len(field.Names) == 0 {
			types = append(types, TypeField{
				Name: names[i],
				Type: typ,
			})
			i++
			continue
		}

		for range field.Names {
			types = append(types, TypeField{
				Name: names[i],
				Type: typ,
			})
			i++
		}
	}

	return
}

func (f *astTypeGenerator) getInputNamesFromAst(inputs []*ast.Field) []string {
	return f.getNamesFromExpr(inputs, "arg")
}

func (f *astTypeGenerator) getOutputNamesFromAst(inputs []*ast.Field) []string {
	return f.getNamesFromExpr(inputs, "out")
}

func (f *astTypeGenerator) getNamesFromExpr(params []*ast.Field, prefix string) []string {
	names := make([]string, 0, len(params))
	i := 0
	for _, p := range params {
		n := len(p.Names)
		if n == 0 {
			n = 1
		}
		for j := 0; j < n; j++ {
			if len(p.Names) > 0 {
				names = append(names, p.Names[j].String())
			} else {
				i++
				names = append(names, fmt.Sprintf("%s%d", prefix, i))
			}
		}
	}
	return names
}

func (f *astTypeGenerator) generateTypeFromMapType(
	mapType *ast.MapType,
	packagePath string,
	importMap map[string]string,
) (MapType, error) {
	keyDef, err := f.generateTypeFromExpr(mapType.Key, packagePath, importMap)
	if err != nil {
		return MapType{}, err
	}

	valDef, err := f.generateTypeFromExpr(mapType.Value, packagePath, importMap)
	if err != nil {
		return MapType{}, err
	}

	return MapType{Key: keyDef, Elem: valDef}, nil
}

func (f *astTypeGenerator) generateTypeFromChanType(
	chanType *ast.ChanType,
	packagePath string,
	importMap map[string]string,
) (ChanType, error) {
	c, err := f.generateTypeFromExpr(chanType.Value, packagePath, importMap)
	if err != nil {
		return ChanType{}, err
	}

	switch chanType.Dir {
	case ast.RECV:
		return ChanType{Dir: ChanTypeDirRecv, Elem: c}, nil
	case ast.SEND:
		return ChanType{Dir: ChanTypeDirSend, Elem: c}, nil
	default:
		return ChanType{Dir: ChanTypeDirBoth, Elem: c}, nil
	}
}

func (f *astTypeGenerator) generateTypeFromStructType(
	structType *ast.StructType,
	packagePath string,
	importMap map[string]string,
) (StructType, error) {
	if structType.Fields == nil {
		return StructType{Fields: nil}, nil
	}

	fields := make([]TypeField, 0, structType.Fields.NumFields())
	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			fieldType, err := f.generateTypeFromExpr(field.Type, packagePath, importMap)
			if err != nil {
				return StructType{}, err
			}

			fields = append(fields, TypeField{
				Name: name.String(),
				Type: fieldType,
			})
		}
	}

	return StructType{Fields: fields}, nil
}

func (f *astTypeGenerator) generateTypeFromInterfaceType(
	interfaceType *ast.InterfaceType,
	packagePath string,
	importMap map[string]string,
) (InterfaceType, error) {
	if interfaceType.Methods == nil {
		return InterfaceType{Methods: nil}, nil
	}

	nMethod := interfaceType.Methods.NumFields()
	methods := make([]InterfaceTypeMethod, 0, nMethod)
	for _, field := range interfaceType.Methods.List {
		switch t := field.Type.(type) {
		case *ast.FuncType:
			name := field.Names[0].String()
			funcTypeAST := field.Type.(*ast.FuncType)
			funcType, err := f.generateTypeFromFuncType(funcTypeAST, packagePath, importMap)
			if err != nil {
				return InterfaceType{}, err
			}
			methods = append(methods, InterfaceTypeMethod{Name: name, Func: funcType})
		case *ast.Ident:
			innerInterface, err := f.GenerateTypesFromSpecs(TypeSpec{PackagePath: packagePath, Name: t.Name})
			if err != nil {
				return InterfaceType{}, err
			}
			methods = append(methods, innerInterface[0].InterfaceType.Methods...)
		case *ast.SelectorExpr:
			x, sel := t.X.(*ast.Ident).Name, t.Sel.Name
			innerInterface, err := f.GenerateTypesFromSpecs(TypeSpec{PackagePath: importMap[x], Name: sel})
			if err != nil {
				return InterfaceType{}, err
			}
			methods = append(methods, innerInterface[0].InterfaceType.Methods...)
		}
	}

	return InterfaceType{Methods: methods}, nil
}
