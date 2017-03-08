package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"text/scanner"
	"time"
)

//go:generate goyacc -p "csp" -o parser.go csp.y

type cspValueMappings map[string]string

type cspChannel struct {
	blockedEvents []string
	needToBlock   bool
	c             chan bool
}

var traceCount int = 0

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

func main() {
	path := flag.String("f", "", "File path to CSP definitions.")
	flagUsage := "Use static trees generated at compile time to handle " +
		"channel input. Mirrors the CSP definition more closely whilst " +
		"using significantly more memory."
	useFormalCommunication = flag.Bool("formalchannels", false, flagUsage)
	flag.Parse()

	if *path == "" {
		log.Fatal("Must specify file to be interpreted using -f flag.")
	}

	file, err := os.Open(*path)
	if err != nil {
		log.Fatalf("%s: \"%s\"", err, *path)
	}
	in := bufio.NewScanner(file)

	log.SetOutput(os.Stdout)
	var lineScan scanner.Scanner
	for in.Scan() {
		lineScan.Init(strings.NewReader(in.Text()))
		cspParse(&cspLex{s: lineScan})
	}
	file.Close()
	if wasParserError {
		return
	}

	err = errorPass()

	if err != nil {
		log.Fatal(err)
	} else if rootNode != nil {
		dummy := cspChannel{nil, true, make(chan bool)}
		rootMap := make(cspValueMappings)
		go interpret_tree(rootNode, &dummy, &rootMap)

		running := true
		for running {
			dummy.c <- false
			running = <-dummy.c
			traceCount++
		}

		if len(rootTrace) < traceCount {
			log.Print("Environment ran out of events.")
		} else {
			log.Print("Unexecuted environment events: ",
				rootTrace[traceCount-1:])
		}
	}
}

func print_tree(node *cspTree) {
	if node != nil {
		log.Printf("%p, %v", node, *node)
		print_tree(node.left)
		print_tree(node.right)
	}
}

func interpret_tree(
	node *cspTree,
	parent *cspChannel,
	mappings *cspValueMappings) {

	if parent.needToBlock {
		<-parent.c
		parent.needToBlock = false
	}

	if len(rootTrace) <= traceCount {
		terminateProcess(parent)
		return
	}
	trace := rootTrace[traceCount]

	switch node.tok {
	case cspParallel:
		left := &cspChannel{nil, false, make(chan bool)}
		right := &cspChannel{nil, false, make(chan bool)}
		leftMap := *mappings
		rightMap := *mappings

		go interpret_tree(node.left, left, &leftMap)
		go interpret_tree(node.right, right, &rightMap)

		parallelMonitor(left, right, parent)
	case cspOr:
		if rand.Intn(2) == 1 {
			interpret_tree(node.right, parent, mappings)
		} else {
			interpret_tree(node.left, parent, mappings)
		}
	case cspChoice:
		if branch, events := choiceTraverse(trace, node); branch != nil {
			interpret_tree(branch, parent, mappings)
		} else if !inAlphabet(node.process, trace) {
			consumeEvent(parent)
			interpret_tree(node, parent, mappings)
		} else {
			fmt := "%s: Deadlock: environment (%s) " +
				"matches none of the choice events %v."
			log.Printf(fmt, node.process, trace, events)
			terminateProcess(parent)
		}
	case cspGenChoice:
		if branches, events := genChoiceTraverse(trace, node); branches != nil {
			bIndex := rand.Intn(len(branches))
			interpret_tree(branches[bIndex], parent, mappings)
		} else if !inAlphabet(node.process, trace) {
			consumeEvent(parent)
			interpret_tree(node, parent, mappings)
		} else {
			fmt := "%s: Deadlock: environment (%s) " +
				"matches none of the general choice events %v."
			log.Printf(fmt, node.process, trace, events)
			terminateProcess(parent)
		}
	case cspEvent:
		if !inAlphabet(node.process, trace) {
			consumeEvent(parent)
			interpret_tree(node, parent, mappings)
		} else {
			if trace != node.ident {
				mappedEvent := (*mappings)[node.ident]

				if trace != mappedEvent {
					fmt := "%s: Deadlock: environment (%s) " +
						"does not match prefixed event (%s)"
					log.Printf(fmt, node.process, trace, node.ident)
					terminateProcess(parent)
					break
				}
			}

			if node.right == nil {
				log.Printf("%s: Process ran out of events.", node.process)

				if parent != nil {
					parent.c <- true
					<-parent.c
					parent.c <- false
				}
				break
			}

			consumeEvent(parent)
			interpret_tree(node.right, parent, mappings)
		}
	case cspProcessTok:
		p, ok := processDefinitions[node.ident]
		if ok {
			interpret_tree(p, parent, mappings)
		} else {
			log.Printf("%s: Process %s is not defined.",
				node.process, node.ident)
			terminateProcess(parent)
		}
	case '!':
		args := strings.Split(trace, ".")
		log.Print("Outputting on ", args[0])
		channels[args[0]] <- args[1]

		consumeEvent(parent)
		interpret_tree(node.right, parent, mappings)
	case '?':
		args := strings.Split(node.ident, ".")
		log.Print("Listening on ", args[0])
		(*mappings)[args[1]] = <-channels[args[0]]

		consumeEvent(parent)
		interpret_tree(node.right, parent, mappings)
	default:
		log.Printf("Unrecognised token %v.", node.tok)
		terminateProcess(parent)
	}
}

func consumeEvent(parent *cspChannel) {
	if parent != nil {
		parent.c <- true
		parent.needToBlock = true
	}
}

func terminateProcess(parent *cspChannel) {
	if parent != nil {
		parent.c <- false
	}
}

func parallelMonitor(left *cspChannel, right *cspChannel, parent *cspChannel) {
	var isLeftDone bool
	for {
		if running := <-left.c; !running {
			isLeftDone = true
			break
		}
		if running := <-right.c; !running {
			isLeftDone = false
			break
		}

		parent.c <- true
		<-parent.c

		left.c <- true
		right.c <- true
	}

	var c chan bool
	running := true
	if isLeftDone {
		c = right.c
		running = <-c
	} else {
		c = left.c
	}
	parent.c <- running

	for running {
		<-parent.c

		c <- true
		running = <-c

		parent.c <- running
	}
}

func choiceTraverse(target string, root *cspTree) (*cspTree, []string) {
	switch root.tok {
	case cspEvent:
		if root.ident == target {
			return root, []string{root.ident}
		} else {
			return nil, []string{root.ident}
		}
	case cspProcessTok:
		return choiceTraverse(target, processDefinitions[root.ident])
	case cspChoice:
		result, leftEvents := choiceTraverse(target, root.left)
		if result != nil {
			return result, leftEvents
		}

		result, rightEvents := choiceTraverse(target, root.right)
		return result, append(leftEvents, rightEvents...)
	case cspGenChoice:
		results, events := genChoiceTraverse(target, root)
		if len(results) > 1 {
			log.Print("Cannot mix a choice with a general choice degenerated " +
				"to nondeterminism.")
			return nil, nil
		} else {
			return results[0], events
		}
	default:
		log.Printf("Mixing a choice operator with a %v is not supported",
			root.tok)
		return nil, nil
	}
}

func genChoiceTraverse(target string, root *cspTree) ([]*cspTree, []string) {
	switch root.tok {
	case cspEvent:
		if root.ident == target {
			return []*cspTree{root}, []string{root.ident}
		} else {
			return nil, []string{root.ident}
		}
	case cspProcessTok:
		return genChoiceTraverse(target, processDefinitions[root.ident])
	case cspChoice:
		branch, events := choiceTraverse(target, root)
		return []*cspTree{branch}, events
	case cspGenChoice:
		leftBranches, leftEvents := genChoiceTraverse(target, root.left)
		rightBranches, rightEvents := genChoiceTraverse(target, root.right)

		return append(leftBranches, rightBranches...),
			append(leftEvents, rightEvents...)
	default:
		fmt := "Mixing a general choice operator with " +
			"a %v is not supported"
		log.Printf(fmt, root.tok)
		return nil, nil
	}
}

func errorPass() error {
	for ident, p := range processDefinitions {
		err := errorPassProcess(ident, p)
		if err != nil {
			return err
		}
	}

	return errorPassProcess("", rootNode)
}

func errorPassProcess(name string, root *cspTree) (err error) {
	brandProcessEvents(name, root)

	err = checkAlphabet(root)
	if err != nil {
		return
	}

	err = checkDeterministicChoice(root)
	if err != nil {
		return
	}

	if root.left != nil {
		err = errorPassProcess(name, root.left)
		if err != nil {
			return
		}
	}

	if root.right != nil {
		err = errorPassProcess(name, root.right)
	}
	return
}

func brandProcessEvents(name string, root *cspTree) {
	root.process = name
}

func checkAlphabet(root *cspTree) error {
	if root.tok == cspEvent {
		if !inAlphabet(root.process, root.ident) {
			errFmt := "Syntax error: Event %s not in %s's alphabet."
			return fmt.Errorf(errFmt, root.ident, root.process)
		}
	} else if root.tok == '?' {
		i := strings.LastIndex(root.ident, ".") + 1
		variable := root.ident[i:]
		if !inAlphabet(root.process, variable) {
			alphabet := alphabets[root.process]
			alphabets[root.process] = append(alphabet, variable)
		}

		channel := root.ident[:i-1]
		for _, cEvent := range channelAlphas[channel] {
			if !inAlphabet(root.process, cEvent) {
				errFmt := "Syntax error: %s's alphabet is not a superset of " +
					"channel %s's alphabet."
				return fmt.Errorf(errFmt, root.process, channel)
			}
		}
	}

	return nil
}

func checkDeterministicChoice(root *cspTree) error {
	if root.tok == cspChoice {
		var left, right string
		switch root.left.tok {
		case cspEvent:
			left = root.left.ident
		case cspProcessTok:
			left = processDefinitions[root.left.ident].ident
		}
		switch root.right.tok {
		case cspEvent:
			right = root.right.ident
		case cspProcessTok:
			right = processDefinitions[root.right.ident].ident
		}

		if left == right {
			errFmt := "Syntax error: Cannot have a choice " +
				"between identical events (%s + %s)."
			return fmt.Errorf(errFmt, left, right)
		}
	}

	return nil
}

func inAlphabet(process string, event string) bool {
	if process == "" {
		return true
	} else {
		alphabet := alphabets[process]
		found := false

		for _, a := range alphabet {
			if a == event {
				found = true
				break
			}
		}

		return found
	}
}
