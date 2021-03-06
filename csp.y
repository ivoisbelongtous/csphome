%{

package main

import (
	"log"
	"strings"
	"text/scanner"
	"unicode"
	"unicode/utf8"
)

type cspTree struct {
	tok int
	ident string
	process string
	branches []*cspTree
}

type cspEventList []string
type cspAlphabetMap map[string]cspEventList

var rootNode *cspTree
var rootTrace cspEventList

var processDefinitions map[string]*cspTree
var alphabets cspAlphabetMap

var channels map[string]chan string
var channelAlphas cspAlphabetMap

var wasParserError bool
var lineNo int = 1

var eventBuf cspEventList

var useFormalCommunication bool = false

%}

%union {
	node *cspTree
	ident string
}

%type <node> Expr Process Event

%token <ident> cspEvent cspProcessTok
%token cspLet cspAlphabetTok cspTraceDef cspChannelDef
%left cspParallel
%left cspChoice
%left cspGenChoice cspOr
%right cspPrefix

%%

Start:
	Expr {rootNode = $1}
	| Decl
	| error {wasParserError = true}
	|

Expr:
	Process {$$ = $1}
	| '(' Expr ')' {$$ = $2}
	| Expr cspChoice Expr {$$ = gatherBinaryBranches(cspChoice, $1, $3)}
	| Expr cspGenChoice Expr {$$ = gatherBinaryBranches(cspGenChoice, $1, $3)}
	| Expr cspOr Expr {$$ = gatherBinaryBranches(cspOr, $1, $3)}
	| Expr cspParallel Expr {$$ = gatherBinaryBranches(cspParallel, $1, $3)}

Process:
	Event
	| cspProcessTok {$$ = &cspTree{tok: cspProcessTok, ident: $1}}
	| Event cspPrefix Process
		{
			$1.branches = []*cspTree{$3}
			$$ = $1
		}
	| Event cspPrefix '(' Expr ')'
		{
			$1.branches = []*cspTree{$4}
			$$ = $1
		}
	| cspEvent '?' cspEvent cspPrefix Process
		{
			if useFormalCommunication {
				inputBranches := make([]*cspTree, len(channelAlphas[$1]))
				inputRoot := &cspTree{tok: cspChoice}
				for i, v := range channelAlphas[$1] {
					inputIdent := $1 + "." + v
					inputProcess := substituteInputVars($1, $3, $5)
					inputBranches[i] = &cspTree {
						tok: cspEvent, ident: inputIdent,
						branches: []*cspTree{inputProcess}}
				}
				$$ = inputRoot
			} else {
				if _, found := channels[$1]; !found {
					channels[$1] = make(chan string)
				}
				$$ = &cspTree{tok: '?', ident: $1+"."+$3,
							  branches: []*cspTree{$5}}
			}
		}
	| cspEvent '?' cspEvent
		{
			if useFormalCommunication {
				$$ = &cspTree{tok: cspEvent, ident: $1+"."+$3}
			} else {
				if _, found := channels[$1]; !found {
					channels[$1] = make(chan string)
				}
				$$ = &cspTree{tok: '?', ident: $1+"."+$3}
			}
		}

Event:
	cspEvent {$$ = &cspTree{tok: cspEvent, ident: $1}}
	| cspEvent '!' cspEvent
		{
			if useFormalCommunication {
				outputIdent := $1 + "." + $3
				$$ = &cspTree{tok: cspEvent, ident: outputIdent}
			} else {
				if _, found := channels[$1]; !found {
					channels[$1] = make(chan string)
				}
				$$ = &cspTree{tok: '!', ident: $1+"."+$3}
			}
		}

Decl:
	cspLet cspAlphabetTok cspProcessTok '=' EventSet
		{
			alphabets[$3] = eventBuf
			eventBuf = nil
		}
	| cspLet cspChannelDef cspEvent '=' EventSet
		{
			channelAlphas[$3] = eventBuf
			eventBuf = nil
		}
	| cspTraceDef EventSet
		{
			rootTrace = eventBuf
			eventBuf = nil
		}
	| cspLet cspProcessTok '=' Expr {processDefinitions[$2] = $4}

EventSet:
	cspEvent {eventBuf = append(eventBuf, $1)}
	| EventSet cspEvent {eventBuf = append(eventBuf, $2)}
	| EventSet ',' cspEvent {eventBuf = append(eventBuf, $3)}

%%

const eof = 0

type cspLex struct {
	s scanner.Scanner
}

func (x *cspLex) Lex(lvalue *cspSymType) (token int) {
	if t := x.peekNextSymbol(); t == 'α' {
		x.s.Next()
		token = cspAlphabetTok
	} else {
		t = x.s.Scan()
		switch t {
		case scanner.Ident: 
			ident := x.s.TokenText()
			switch ident {
			case "let":
				token = cspLet
			case "tracedef":
				token = cspTraceDef
			case "alphadef":
				token = cspAlphabetTok
			case "chandef", "channeldef":
				token = cspChannelDef
			default:
				r, _ := utf8.DecodeRuneInString(ident)
				if unicode.IsUpper(r) {
					token = cspProcessTok
				} else {
					token = cspEvent
					if x.peekNextSymbol() == '.' {
						x.s.Scan()
						x.s.Scan()
						ident = ident + "." + x.s.TokenText()
					}
				}
			}
			lvalue.ident = ident
		case scanner.Int:
			token = cspEvent
			lvalue.ident = x.s.TokenText()
		case '-':
			if x.s.Peek() != '>' {
				log.Printf("Unrecognised character: -")
			} else {
				x.s.Next()
				token = cspPrefix
			}
		case '[':
			switch x.s.Peek() {
			case '|':
				x.s.Next()
				if x.s.Peek() != ']' {
					log.Printf("Unrecognised sequence: \"[|\"")
				} else {
					x.s.Next()
					token = cspGenChoice
				}
			case ']':
				x.s.Next()
				token = cspOr
			default:
				log.Printf("Unrecognised character: [")
			}
		case '|':
			if x.s.Peek() != '|' {
				token = cspChoice
			} else {
				x.s.Next()
				token = cspParallel
			}
		case scanner.EOF:
			lineNo++
			token = eof
		case '=', ',', '!', '?', '(', ')':
			token = int(t)
		default:
			log.Printf("Unrecognised character: %q", t)
		}
	}

	return
}

func (x *cspLex) peekNextSymbol() rune {
	for {
		s := x.s.Peek()
		if unicode.IsSpace(s) {
			x.s.Next()
		} else {
			return s
		}
	}
}

func (x *cspLex) Error(s string) {
	if cspErrorVerbose {
		log.Printf("Parse error at line %v (%s)", lineNo, s)
	} else {
		log.Printf("Parse error at line %v", lineNo)
	}
}

func gatherBinaryBranches(tok int, left, right *cspTree) (out *cspTree) {
	if (left.tok == tok) {
		out = left
	} else {
		out = &cspTree{tok: tok, branches: []*cspTree{left}}
	}

	if (right.tok == tok) {
		out.branches = append(out.branches, right.branches...)
	} else {
		out.branches = append(out.branches, right)
	}

	return
}

func substituteInputVars(oldI string, newI string, root *cspTree) *cspTree {
	if root.tok == cspEvent {
		if root.ident == oldI {
			root.ident = newI
		} else if strings.HasSuffix(root.ident, "."+oldI) {
			root.ident = strings.TrimSuffix(root.ident, "."+oldI) + "." + newI
		}
	}

	branchCopy := make([]*cspTree, len(root.branches))
	for i := 0; i < len(root.branches); i++ {
		branchCopy[i] = substituteInputVars(oldI, newI, root.branches[i])
	}

	nodeCopy := *root
	nodeCopy.branches = branchCopy
	return &nodeCopy
}
