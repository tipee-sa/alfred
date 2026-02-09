package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatCommandLine_Empty(t *testing.T) {
	assert.Equal(t, "", formatCommandLine("", 80))
}

func TestFormatCommandLine_Short(t *testing.T) {
	got := formatCommandLine("alfred run job.yaml", 80)
	assert.Equal(t, "  alfred run job.yaml", got)
}

func TestFormatCommandLine_SingleParam(t *testing.T) {
	cmd := "bin/alfred run build.yaml -p target=origin/develop instance1 instance2"
	got := formatCommandLine(cmd, 80)
	expected := "  bin/alfred run build.yaml \\\n" +
		"    -p target=origin/develop \\\n" +
		"    instance1 instance2"
	assert.Equal(t, expected, got)
}

func TestFormatCommandLine_MultipleParams(t *testing.T) {
	cmd := "bin/alfred run build.yaml -p target=origin/develop -p mode=fast instance1 instance2"
	got := formatCommandLine(cmd, 80)
	expected := "  bin/alfred run build.yaml \\\n" +
		"    -p target=origin/develop \\\n" +
		"    -p mode=fast \\\n" +
		"    instance1 instance2"
	assert.Equal(t, expected, got)
}

func TestFormatCommandLine_ParamsWithoutInstances(t *testing.T) {
	cmd := "bin/alfred run build.yaml -p target=origin/develop"
	got := formatCommandLine(cmd, 80)
	expected := "  bin/alfred run build.yaml \\\n" +
		"    -p target=origin/develop"
	assert.Equal(t, expected, got)
}

func TestFormatCommandLine_LongInstanceListWraps(t *testing.T) {
	cmd := "bin/alfred run build.yaml -p target=develop i1 i2 i3 i4 i5 i6 i7 i8 i9 i10 i11 i12 i13 i14 i15 i16 i17 i18 i19 i20"
	got := formatCommandLine(cmd, 60)
	expected := "  bin/alfred run build.yaml \\\n" +
		"    -p target=develop \\\n" +
		"    i1 i2 i3 i4 i5 i6 i7 i8 i9 i10 i11 i12 i13 i14 i15 i16 \\\n" +
		"    i17 i18 i19 i20"
	assert.Equal(t, expected, got)
}

func TestFormatCommandLine_NoParamsWrapsAtMaxWidth(t *testing.T) {
	got := formatCommandLine("a b c d e f", 10)
	expected := "  a b c \\\n    d e \\\n    f"
	assert.Equal(t, expected, got)
}

func TestFormatCommandLine_OneArgTooLong(t *testing.T) {
	got := formatCommandLine("averylongcommandname", 10)
	assert.Equal(t, "  averylongcommandname", got)
}

func TestFormatCommandLine_ParamValueNotSplit(t *testing.T) {
	// Even if -p value would exceed maxWidth, they stay on the same line
	cmd := "alfred run job.yaml -p very-long-key=very-long-value"
	got := formatCommandLine(cmd, 30)
	expected := "  alfred run job.yaml \\\n" +
		"    -p very-long-key=very-long-value"
	assert.Equal(t, expected, got)
}

func TestShellFields_Empty(t *testing.T) {
	assert.Empty(t, shellFields(""))
}

func TestShellFields_Simple(t *testing.T) {
	assert.Equal(t, []string{"alfred", "run", "job.yaml"}, shellFields("alfred run job.yaml"))
}

func TestShellFields_SingleQuoted(t *testing.T) {
	assert.Equal(t, []string{"alfred", "-p", "KEY=value with spaces"}, shellFields("alfred -p 'KEY=value with spaces'"))
}

func TestShellFields_MultipleSpaces(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, shellFields("  a   b   c  "))
}

func TestShellFields_QuotedEmpty(t *testing.T) {
	// shellescape.Quote("") produces ''
	assert.Equal(t, []string{"alfred", ""}, shellFields("alfred ''"))
}

func TestShellFields_MixedQuotedAndUnquoted(t *testing.T) {
	assert.Equal(t,
		[]string{"bin/alfred", "run", "job.yaml", "-p", "target=origin/develop", "-p", "msg=hello world", "instance1"},
		shellFields("bin/alfred run job.yaml -p target=origin/develop -p 'msg=hello world' instance1"),
	)
}

func TestShellFields_AdjacentQuotes(t *testing.T) {
	// e.g. prefix'quoted part' → prefixquoted part (single token)
	assert.Equal(t, []string{"prefixquoted part"}, shellFields("prefix'quoted part'"))
}
