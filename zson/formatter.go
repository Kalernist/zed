package zson

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/brimdata/zed"
	"github.com/brimdata/zed/pkg/terminal/color"
	"github.com/brimdata/zed/zcode"
)

type Formatter struct {
	typedefs  typemap
	permanent typemap
	persist   *regexp.Regexp
	tab       int
	newline   string
	typeTab   int
	nid       int
	builder   strings.Builder
	stack     []strings.Builder
	implied   map[zed.Type]bool
	colors    color.Stack
}

func NewFormatter(pretty int, persist *regexp.Regexp) *Formatter {
	var newline string
	if pretty > 0 {
		newline = "\n"
	}
	var permanent typemap
	if persist != nil {
		permanent = make(typemap)
	}
	return &Formatter{
		typedefs:  make(typemap),
		permanent: permanent,
		tab:       pretty,
		newline:   newline,
		implied:   make(map[zed.Type]bool),
		persist:   persist,
	}
}

// Persist matches type names to the regular expression provided and
// persists the matched types across records in the stream.  This is useful
// when typedefs have complicated type signatures, e.g., as generated
// by fused fields of records creating a union of records.
func (f *Formatter) Persist(re *regexp.Regexp) {
	f.permanent = make(typemap)
	f.persist = re
}

func (f *Formatter) push() {
	f.stack = append(f.stack, f.builder)
	f.builder = strings.Builder{}
}

func (f *Formatter) pop() {
	n := len(f.stack)
	f.builder = f.stack[n-1]
	f.stack = f.stack[:n-1]
}

func (f *Formatter) FormatRecord(rec *zed.Value) (string, error) {
	f.builder.Reset()
	// We reset tyepdefs so named types are emitted with their
	// definition at first use in each record according to the
	// left-to-right DFS order.  We could make this more efficient
	// by putting a record number/nonce in the map but ZSON
	// is already intended to be the low performance path.
	f.typedefs = make(typemap)
	if err := f.formatValueAndDecorate(rec.Type, rec.Bytes); err != nil {
		return "", err
	}
	return f.builder.String(), nil
}

func FormatValue(zv zed.Value) (string, error) {
	f := NewFormatter(0, nil)
	return f.Format(zv)
}

func MustFormatValue(zv zed.Value) string {
	s, err := FormatValue(zv)
	if err != nil {
		panic(fmt.Errorf("zson format value failed: type %s, bytes %s", zv.Type, hex.EncodeToString(zv.Bytes)))
	}
	return s
}

func String(zv zed.Value) string {
	s, err := FormatValue(zv)
	if err != nil {
		s = fmt.Sprintf("<zng parse err: %s>", err)
	}
	return s
}

func (f *Formatter) Format(zv zed.Value) (string, error) {
	f.builder.Reset()
	if err := f.formatValueAndDecorate(zv.Type, zv.Bytes); err != nil {
		return "", err
	}
	return f.builder.String(), nil
}

func (f *Formatter) hasName(typ zed.Type) bool {
	ok := f.typedefs.exists(typ)
	if !ok && f.persist != nil {
		ok = f.permanent.exists(typ)
	}
	return ok
}

func (f *Formatter) nameOf(typ zed.Type) string {
	s := f.typedefs[typ]
	if s == "" && f.permanent != nil {
		s = f.permanent[typ]
	}
	return s
}

func (f *Formatter) saveType(alias *zed.TypeAlias) {
	name := alias.Name
	f.typedefs[alias] = name
	if f.permanent != nil && f.persist.MatchString(name) {
		f.permanent[alias] = name
	}
}

func (f *Formatter) formatValueAndDecorate(typ zed.Type, bytes zcode.Bytes) error {
	known := f.hasName(typ)
	implied := f.isImplied(typ)
	if err := f.formatValue(0, typ, bytes, known, implied, false); err != nil {
		return err
	}
	f.decorate(typ, false, bytes == nil)
	return nil
}

func (f *Formatter) formatValue(indent int, typ zed.Type, bytes zcode.Bytes, parentKnown, parentImplied, decorate bool) error {
	known := parentKnown || f.hasName(typ)
	if bytes == nil {
		f.build("null")
		if parentImplied {
			parentKnown = false
		}
		if decorate {
			f.decorate(typ, parentKnown, true)
		}
		return nil
	}
	var err error
	var null bool
	switch t := typ.(type) {
	default:
		f.startColorPrimitive(typ)
		f.build(typ.Format(bytes))
		f.endColor()
	case *zed.TypeAlias:
		err = f.formatValue(indent, t.Type, bytes, known, parentImplied, false)
	case *zed.TypeRecord:
		err = f.formatRecord(indent, t, bytes, known, parentImplied)
	case *zed.TypeArray:
		null, err = f.formatVector(indent, "[", "]", t.Type, zed.Value{t, bytes}, known, parentImplied)
	case *zed.TypeSet:
		null, err = f.formatVector(indent, "|[", "]|", t.Type, zed.Value{t, bytes}, known, parentImplied)
	case *zed.TypeUnion:
		err = f.formatUnion(indent, t, bytes)
	case *zed.TypeMap:
		null, err = f.formatMap(indent, t, bytes, known, parentImplied)
	case *zed.TypeEnum:
		f.build(t.Format(bytes))
	case *zed.TypeOfType:
		f.startColorPrimitive(zed.TypeType)
		f.buildf("(%s)", zed.FormatTypeValue(bytes))
		f.endColor()
	}
	if err == nil && decorate {
		f.decorate(typ, parentKnown, null)
	}
	return err
}

func (f *Formatter) nextInternalType() string {
	name := strconv.Itoa(f.nid)
	f.nid++
	return name
}

func (f *Formatter) decorate(typ zed.Type, known, null bool) {
	if known || (!(null && typ != zed.TypeNull) && f.isImplied(typ)) {
		return
	}
	f.startColor(color.Gray(200))
	defer f.endColor()
	if name := f.nameOf(typ); name != "" {
		if f.tab > 0 {
			f.build(" ")
		}
		f.buildf("(%s)", name)
	} else if SelfDescribing(typ) && !null {
		if typ, ok := typ.(*zed.TypeAlias); ok {
			f.saveType(typ)
			if f.tab > 0 {
				f.build(" ")
			}
			f.buildf("(=%s)", typ.Name)
		}
	} else {
		if f.tab > 0 {
			f.build(" ")
		}
		f.build("(")
		f.formatType(typ)
		f.build(")")
	}
}

func (f *Formatter) formatRecord(indent int, typ *zed.TypeRecord, bytes zcode.Bytes, known, parentImplied bool) error {
	f.build("{")
	if len(typ.Columns) == 0 {
		f.build("}")
		return nil
	}
	indent += f.tab
	sep := f.newline
	it := bytes.Iter()
	for _, field := range typ.Columns {
		if it.Done() {
			return &zed.RecordTypeError{Name: string(field.Name), Type: field.Type.String(), Err: zed.ErrMissingField}
		}
		bytes, _ := it.Next()
		f.build(sep)
		f.startColor(color.Blue)
		f.indent(indent, zed.QuotedName(field.Name))
		f.endColor()
		f.build(":")
		if f.tab > 0 {
			f.build(" ")
		}
		if err := f.formatValue(indent, field.Type, bytes, known, parentImplied, true); err != nil {
			return err
		}
		sep = "," + f.newline
	}
	f.build(f.newline)
	f.indent(indent-f.tab, "}")
	return nil
}

func (f *Formatter) formatVector(indent int, open, close string, inner zed.Type, zv zed.Value, known, parentImplied bool) (bool, error) {
	f.build(open)
	len, err := zv.ContainerLength()
	if err != nil {
		return true, err
	}
	if len == 0 {
		f.build(close)
		return true, nil
	}
	indent += f.tab
	sep := f.newline
	it := zv.Iter()
	for !it.Done() {
		f.build(sep)
		f.indent(indent, "")
		bytes, _ := it.Next()
		if err := f.formatValue(indent, inner, bytes, known, parentImplied, true); err != nil {
			return true, err
		}
		sep = "," + f.newline
	}
	f.build(f.newline)
	f.indent(indent-f.tab, close)
	return false, nil
}

func (f *Formatter) formatUnion(indent int, union *zed.TypeUnion, bytes zcode.Bytes) error {
	typ, _, bytes, err := union.SplitZNG(bytes)
	if err != nil {
		return err
	}
	// XXX For now, we always decorate a union value so that
	// we can determine the selector from the value's explicit type.
	// We can later optimize this so we only print the decorator if its
	// ambigous with another type (e.g., int8 and int16 vs a union of int8 and string).
	// Let's do this after we have the parser working and capable of this
	// disambiguation.  See issue #1764.
	// In other words, just because we known the union's type doesn't mean
	// we know the type of a particular value of that union.
	const known = false
	const parentImplied = true
	return f.formatValue(indent, typ, bytes, known, parentImplied, true)
}

func (f *Formatter) formatMap(indent int, typ *zed.TypeMap, bytes zcode.Bytes, known, parentImplied bool) (bool, error) {
	empty := true
	f.build("|{")
	indent += f.tab
	sep := f.newline
	for it := bytes.Iter(); !it.Done(); {
		keyBytes, _ := it.Next()
		if it.Done() {
			return empty, errors.New("truncated map value")
		}
		empty = false
		valBytes, _ := it.Next()
		f.build(sep)
		f.indent(indent, "")
		if err := f.formatValue(indent, typ.KeyType, keyBytes, known, parentImplied, true); err != nil {
			return empty, err
		}
		if zed.TypeUnder(typ.KeyType) == zed.TypeIP && len(keyBytes) == 16 {
			// To avoid ambiguity, whitespace must separate an IPv6
			// map key from the colon that follows it.
			f.build(" ")
		}
		f.build(":")
		if f.tab > 0 {
			f.build(" ")
		}
		if err := f.formatValue(indent, typ.ValType, valBytes, known, parentImplied, true); err != nil {
			return empty, err
		}
		sep = "," + f.newline
	}
	f.build(f.newline)
	f.indent(indent-f.tab, "}|")
	return empty, nil
}

func (f *Formatter) indent(tab int, s string) {
	for k := 0; k < tab; k++ {
		f.builder.WriteByte(' ')
	}
	f.build(s)
}

func (f *Formatter) build(s string) {
	f.builder.WriteString(s)
}

func (f *Formatter) buildf(s string, args ...interface{}) {
	f.builder.WriteString(fmt.Sprintf(s, args...))
}

// formatType builds typ as a type string with any
// needed typedefs for aliases that have not been previously defined.
// These typedefs use the embedded syntax (name=(type-string)).
// Typedefs handled by decorators are handled in decorate().
// The routine re-enters the type formatter with a fresh builder by
// invoking push()/pop().
func (f *Formatter) formatType(typ zed.Type) {
	if name := f.nameOf(typ); name != "" {
		f.build(name)
		return
	}
	if alias, ok := typ.(*zed.TypeAlias); ok {
		f.saveType(alias)
		f.build(alias.Name)
		f.build("=(")
		f.formatType(alias.Type)
		f.build(")")
		return
	}
	if typ.ID() < zed.IDTypeDef {
		f.build(typ.String())
		return
	}
	f.push()
	f.formatTypeBody(typ)
	s := f.builder.String()
	f.pop()
	f.build(s)
}

func (f *Formatter) formatTypeBody(typ zed.Type) error {
	if name := f.nameOf(typ); name != "" {
		f.build(name)
		return nil
	}
	switch typ := typ.(type) {
	case *zed.TypeAlias:
		// Aliases are handled differently above to determine the
		// plain form vs embedded typedef.
		panic("alias shouldn't be formatted")
	case *zed.TypeRecord:
		f.formatTypeRecord(typ)
	case *zed.TypeArray:
		f.build("[")
		f.formatType(typ.Type)
		f.build("]")
	case *zed.TypeSet:
		f.build("|[")
		f.formatType(typ.Type)
		f.build("]|")
	case *zed.TypeMap:
		f.build("|{")
		f.formatType(typ.KeyType)
		f.build(":")
		f.formatType(typ.ValType)
		f.build("}|")
	case *zed.TypeUnion:
		f.formatTypeUnion(typ)
	case *zed.TypeEnum:
		return f.formatTypeEnum(typ)
	case *zed.TypeOfType:
		formatType(&f.builder, make(typemap), typ)
	default:
		panic("unknown case in formatTypeBody(): " + typ.String())
	}
	return nil
}

func (f *Formatter) formatTypeRecord(typ *zed.TypeRecord) {
	f.build("{")
	for k, col := range typ.Columns {
		if k > 0 {
			f.build(",")
		}
		f.build(zed.QuotedName(col.Name))
		f.build(":")
		f.formatType(col.Type)
	}
	f.build("}")
}

func (f *Formatter) formatTypeUnion(typ *zed.TypeUnion) {
	f.build("(")
	for k, typ := range typ.Types {
		if k > 0 {
			f.build(",")
		}
		f.formatType(typ)
	}
	f.build(")")
}

func (f *Formatter) formatTypeEnum(typ *zed.TypeEnum) error {
	f.build("<")
	for k, s := range typ.Symbols {
		if k > 0 {
			f.build(",")
		}
		f.buildf("%s", zed.QuotedName(s))
	}
	f.build(">")
	return nil
}

var colors = map[zed.Type]color.Code{
	zed.TypeString: color.Green,
	zed.TypeError:  color.Red,
	zed.TypeType:   color.Orange,
}

func (f *Formatter) startColorPrimitive(typ zed.Type) {
	if f.tab > 0 {
		c, ok := colors[zed.TypeUnder(typ)]
		if !ok {
			c = color.Reset
		}
		f.startColor(c)
	}
}

func (f *Formatter) startColor(code color.Code) {
	if f.tab > 0 {
		f.colors.Start(&f.builder, code)
	}
}

func (f *Formatter) endColor() {
	if f.tab > 0 {
		f.colors.End(&f.builder)
	}
}

func (f *Formatter) isImplied(typ zed.Type) bool {
	implied, ok := f.implied[typ]
	if !ok {
		implied = Implied(typ)
		f.implied[typ] = implied
	}
	return implied
}

type typemap map[zed.Type]string

func (t typemap) exists(typ zed.Type) bool {
	_, ok := t[typ]
	return ok
}

func (t typemap) known(typ zed.Type) bool {
	if _, ok := t[typ]; ok {
		return true
	}
	if _, ok := typ.(*zed.TypeOfType); ok {
		return true
	}
	if _, ok := typ.(*zed.TypeAlias); ok {
		return false
	}
	return typ.ID() < zed.IDTypeDef
}

// FormatType formats a type in canonical form to represent type values
// as standalone entities.
func FormatType(typ zed.Type) string {
	var b strings.Builder
	formatType(&b, make(typemap), typ)
	return b.String()
}

func formatType(b *strings.Builder, typedefs typemap, typ zed.Type) {
	if name, ok := typedefs[typ]; ok {
		b.WriteString(name)
		return
	}
	switch t := typ.(type) {
	case *zed.TypeAlias:
		name := t.Name
		b.WriteString(name)
		if _, ok := typedefs[typ]; !ok {
			typedefs[typ] = name
			b.WriteString("=(")
			formatType(b, typedefs, t.Type)
			b.WriteByte(')')
		}
	case *zed.TypeRecord:
		b.WriteByte('{')
		for k, col := range t.Columns {
			if k > 0 {
				b.WriteByte(',')
			}
			b.WriteString(zed.QuotedName(col.Name))
			b.WriteString(":")
			formatType(b, typedefs, col.Type)
		}
		b.WriteByte('}')
	case *zed.TypeArray:
		b.WriteByte('[')
		formatType(b, typedefs, t.Type)
		b.WriteByte(']')
	case *zed.TypeSet:
		b.WriteString("|[")
		formatType(b, typedefs, t.Type)
		b.WriteString("]|")
	case *zed.TypeMap:
		b.WriteString("|{")
		formatType(b, typedefs, t.KeyType)
		b.WriteByte(':')
		formatType(b, typedefs, t.ValType)
		b.WriteString("}|")
	case *zed.TypeUnion:
		b.WriteByte('(')
		for k, typ := range t.Types {
			if k > 0 {
				b.WriteByte(',')
			}
			formatType(b, typedefs, typ)
		}
		b.WriteByte(')')
	case *zed.TypeEnum:
		b.WriteByte('<')
		for k, s := range t.Symbols {
			if k > 0 {
				b.WriteByte(',')
			}
			b.WriteString(zed.QuotedName(s))
		}
		b.WriteByte('>')
	default:
		b.WriteString(typ.String())
	}
}
