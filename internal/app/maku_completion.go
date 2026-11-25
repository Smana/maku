// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const completionUsage = "usage: maku completion <bash|zsh|fish>"

// compFlag is one flag of a completion key: whether it takes a value, and the values when the usage
// spells them (`--format md|text`).
type compFlag struct {
	name   string
	takes  bool
	values []string
}

// compKey is what completes after one command, or after one command and its subcommand
// ("policy list"): its flags, and the values of its positional argument when the usage spells
// them (`setup <claude|opencode|...>`).
type compKey struct {
	key    string
	flags  []compFlag
	values []string
}

// compSpec is the whole completion: the top-level commands, each command's subcommands, and the keys.
type compSpec struct {
	commands []string
	subs     map[string][]string
	keys     []*compKey
}

// completionExtras are the flags a usage line describes in prose rather than in its synopsis.
var completionExtras = map[string][]compFlag{
	"policy validate": {{name: "--output", takes: true, values: []string{"json"}}},
}

// usageDescCol is the column makuUsage's descriptions start at; a less indented line continues a synopsis.
const usageDescCol = 34

// completionSpec reads the completion from the usage text, so that a command added to the usage is
// completed with no second list to keep.
func completionSpec(usage string) compSpec {
	s := compSpec{subs: map[string][]string{}}
	byKey := map[string]*compKey{}
	key := func(k string) *compKey {
		if byKey[k] == nil {
			byKey[k] = &compKey{key: k}
			s.keys = append(s.keys, byKey[k])
		}
		return byKey[k]
	}
	var cur []string // the keys the current synopsis feeds
	for _, line := range strings.Split(usage, "\n") {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		synopsis, _, _ := strings.Cut(strings.TrimSpace(line), "  ")
		toks := strings.Fields(synopsis)
		switch {
		case indent == 2 && len(toks) > 0:
			cmd := toks[0]
			if !slices.Contains(s.commands, cmd) {
				s.commands = append(s.commands, cmd)
			}
			cur = []string{cmd}
			toks = toks[1:]
			if len(toks) > 0 && isWordAlternatives(toks[0]) {
				cur = nil
				for _, sub := range strings.Split(toks[0], "|") {
					if !slices.Contains(s.subs[cmd], sub) {
						s.subs[cmd] = append(s.subs[cmd], sub)
					}
					cur = append(cur, cmd+" "+sub)
				}
				toks = toks[1:]
			}
		case indent > 2 && indent < usageDescCol && cur != nil:
		default:
			continue
		}
		flagsOf, values := synopsisFlags(toks)
		for _, k := range cur {
			ck := key(k)
			for _, f := range flagsOf {
				if !slices.ContainsFunc(ck.flags, func(g compFlag) bool { return g.name == f.name }) {
					ck.flags = append(ck.flags, f)
				}
			}
			ck.values = append(ck.values, values...)
		}
	}
	for k, extra := range completionExtras {
		key(k).flags = append(key(k).flags, extra...)
	}
	s.commands = append(s.commands, "help")
	return s
}

// isWordAlternatives is a subcommand token: `list` or `validate|export|check`.
func isWordAlternatives(tok string) bool {
	for _, w := range strings.Split(tok, "|") {
		if w == "" || w[0] < 'a' || w[0] > 'z' || strings.ContainsFunc(w, func(r rune) bool {
			return (r < 'a' || r > 'z') && r != '-'
		}) {
			return false
		}
	}
	return true
}

// synopsisFlags reads a synopsis's flags and the spelled values of its positional argument.
func synopsisFlags(toks []string) ([]compFlag, []string) {
	var fl []compFlag
	var values []string
	for i, raw := range toks {
		tok := strings.TrimSuffix(raw, "...")
		closes := strings.HasSuffix(tok, "]")
		tok = strings.Trim(tok, "[]")
		if strings.HasPrefix(tok, "<") && strings.Contains(tok, "|") {
			values = append(values, strings.Split(strings.Trim(tok, "<>"), "|")...)
			continue
		}
		if !strings.HasPrefix(tok, "--") {
			continue
		}
		name, _, _ := strings.Cut(tok, "=")
		f := compFlag{name: name}
		if !closes && !strings.Contains(tok, "=") && i+1 < len(toks) {
			next := toks[i+1]
			if !strings.HasPrefix(next, "[") && !strings.HasPrefix(next, "-") && next != "|" {
				f.takes = true
				if v := strings.Trim(strings.TrimSuffix(next, "..."), "[]<>"); strings.Contains(v, "|") && isWordAlternatives(v) {
					f.values = strings.Split(v, "|")
				}
			}
		}
		fl = append(fl, f)
	}
	return fl, values
}

// completion prints the completion script of one shell.
func (r Maku) completion(args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprintln(r.Out, completionUsage)
		return err
	}
	if len(args) != 1 {
		return errors.New(completionUsage)
	}
	s := completionSpec(makuUsage)
	switch args[0] {
	case "bash":
		return s.bash(r.Out)
	case "zsh":
		return s.zsh(r.Out)
	case "fish":
		return s.fish(r.Out)
	}
	return fmt.Errorf("unknown shell %q\n%s", args[0], completionUsage)
}

func (s compSpec) subCommands() []string {
	var cmds []string
	for _, c := range s.commands {
		if len(s.subs[c]) > 0 {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

func flagNames(fl []compFlag) string {
	names := make([]string, len(fl))
	for i, f := range fl {
		names[i] = f.name
	}
	return strings.Join(names, " ")
}

// shCases writes the case arms bash and zsh share: a flag's values, then a key's flags, then its
// positional values. lit is the shell's word-list command, files its action for a free value.
func (s compSpec) shCases(w io.Writer, lit func(string) string, files string) {
	_, _ = fmt.Fprintf(w, "  case \"$key $prev\" in\n")
	var free []string // the flags whose value is free text or a path
	for _, k := range s.keys {
		for _, f := range k.flags {
			switch {
			case len(f.values) > 0:
				_, _ = fmt.Fprintf(w, "    (%q) %s; return ;;\n", k.key+" "+f.name, lit(strings.Join(f.values, " ")))
			case f.takes:
				free = append(free, fmt.Sprintf("%q", k.key+" "+f.name))
			}
		}
	}
	if len(free) > 0 {
		_, _ = fmt.Fprintf(w, "    (%s)\n      %s ;;\n", strings.Join(free, "|\\\n     "), files)
	}
	_, _ = fmt.Fprintf(w, "  esac\n  if [[ $cur == -* ]]; then\n    case \"$key\" in\n")
	for _, k := range s.keys {
		if len(k.flags) > 0 {
			_, _ = fmt.Fprintf(w, "      (%q) %s ;;\n", k.key, lit(flagNames(k.flags)))
		}
	}
	_, _ = fmt.Fprintf(w, "    esac\n    return\n  fi\n  case \"$key\" in\n")
	for _, k := range s.keys {
		if len(k.values) > 0 {
			_, _ = fmt.Fprintf(w, "    (%q) %s; return ;;\n", k.key, lit(strings.Join(k.values, " ")))
		}
	}
	_, _ = fmt.Fprintf(w, "  esac\n")
}

func (s compSpec) bash(w io.Writer) error {
	lit := func(words string) string { return fmt.Sprintf("COMPREPLY=($(compgen -W %q -- \"$cur\"))", words) }
	_, err := fmt.Fprintf(w, `# bash completion for maku: source <(maku completion bash)
_maku() {
  local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]} key=${COMP_WORDS[1]}
  COMPREPLY=()
  if (( COMP_CWORD == 1 )); then
    %s
    return
  fi
  case "$key" in
    (%s)
      if (( COMP_CWORD == 2 )); then
        case "$key" in
`, lit(strings.Join(s.commands, " ")), strings.Join(s.subCommands(), "|"))
	if err != nil {
		return err
	}
	for _, c := range s.subCommands() {
		_, _ = fmt.Fprintf(w, "          (%s) %s ;;\n", c, lit(strings.Join(s.subs[c], " ")))
	}
	_, _ = fmt.Fprintf(w, "        esac\n        return\n      fi\n      key=\"$key ${COMP_WORDS[2]}\" ;;\n  esac\n")
	s.shCases(w, lit, "return")
	_, err = fmt.Fprintf(w, "}\ncomplete -o default -F _maku maku\n")
	return err
}

func (s compSpec) zsh(w io.Writer) error {
	lit := func(words string) string { return "compadd -- " + words }
	_, err := fmt.Fprintf(w, `#compdef maku
# zsh completion for maku: source <(maku completion zsh), or save it as _maku on $fpath
_maku() {
  local cur=${words[CURRENT]} prev=${words[CURRENT-1]} key=${words[2]}
  if (( CURRENT == 2 )); then
    %s
    return
  fi
  case "$key" in
    (%s)
      if (( CURRENT == 3 )); then
        case "$key" in
`, lit(strings.Join(s.commands, " ")), strings.Join(s.subCommands(), "|"))
	if err != nil {
		return err
	}
	for _, c := range s.subCommands() {
		_, _ = fmt.Fprintf(w, "          (%s) %s ;;\n", c, lit(strings.Join(s.subs[c], " ")))
	}
	_, _ = fmt.Fprintf(w, "        esac\n        return\n      fi\n      key=\"$key ${words[3]}\" ;;\n  esac\n")
	s.shCases(w, lit, "_files; return")
	_, err = fmt.Fprintf(w, `  _files
}
if [[ ${zsh_eval_context[-1]} == loadautofunc ]]; then
  _maku "$@"
else
  compdef _maku maku
fi
`)
	return err
}

func (s compSpec) fish(w io.Writer) error {
	_, err := fmt.Fprintf(w, `# fish completion for maku: maku completion fish | source
# __maku_in: the words before the cursor start with the given command (and subcommand).
function __maku_in
    set -l t (commandline -opc)
    set -e t[1]
    test (count $t) -ge (count $argv); and test "$t[1..(count $argv)]" = "$argv"
end
# __maku_at: the words before the cursor are exactly the given ones.
function __maku_at
    set -l t (commandline -opc)
    set -e t[1]
    test "$t" = "$argv"
end
complete -c maku -f -n 'test (count (commandline -opc)) -eq 1' -a %q
`, strings.Join(s.commands, " "))
	if err != nil {
		return err
	}
	for _, c := range s.subCommands() {
		_, _ = fmt.Fprintf(w, "complete -c maku -f -n '__maku_at %s' -a %q\n", c, strings.Join(s.subs[c], " "))
	}
	for _, k := range s.keys {
		for _, f := range k.flags {
			arg := ""
			switch {
			case len(f.values) > 0:
				arg = fmt.Sprintf(" -x -a %q", strings.Join(f.values, " "))
			case f.takes:
				arg = " -r"
			}
			_, _ = fmt.Fprintf(w, "complete -c maku -n '__maku_in %s' -l %s%s\n", k.key, strings.TrimPrefix(f.name, "--"), arg)
		}
		if len(k.values) > 0 {
			_, _ = fmt.Fprintf(w, "complete -c maku -f -n '__maku_at %s' -a %q\n", k.key, strings.Join(k.values, " "))
		}
	}
	return nil
}
