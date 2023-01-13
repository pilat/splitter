package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/inconshreveable/log15"
	"github.com/pilat/splitter/pkg/github"
)

const dtFormat = "2006-01-02T15:04:05Z"

type RSpec struct {
	XMLName   xml.Name        `xml:"testsuite"`
	TestCases []RSpecTestCase `xml:"testcase"`
}

type RSpecTestCase struct {
	XMLName xml.Name `xml:"testcase"`
	Name    string   `xml:"name,attr"`
	File    string   `xml:"file,attr"`
	Time    float64  `xml:"time,attr"`
}

type Tests struct {
	Filename string
	Name     string
	Time     float64
}

func main() {
	ctx := context.Background()

	handler := log.Handler(log.StreamHandler(os.Stderr, log.TerminalFormat()))
	log.Root().SetHandler(handler)

	log := log.New()

	var inputFiles []string
	inputFile := os.Getenv("INPUT_FROM_FILE")
	if inputFile == "" {
		stdin, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Error("failed to read stdin", "err", err)
			os.Exit(1)
		}
		inputFiles = strings.Split(string(stdin), "\n")
	} else {
		// write open inputFile and read lines into inputFiles
		data, err := os.ReadFile(inputFile)
		if err != nil {
			log.Error("failed to read input", "err", err)
			os.Exit(1)
		}

		inputFiles = strings.Split(string(data), "\n")
	}

	inputFilesTemp := make([]string, 0)
	for _, inputFile := range inputFiles {
		if inputFile != "" {
			inputFilesTemp = append(inputFilesTemp, inputFile)
		}
	}
	inputFiles = inputFilesTemp

	maxNodes, err := strconv.Atoi(os.Getenv("NODES_COUNT")) // 10
	if err != nil {
		log.Error("failed to parse NODES_COUNT", "err", err)
		os.Exit(1)
	}

	currentNode, err := strconv.Atoi(os.Getenv("NODE_ID")) // From 0 to 9
	if err != nil {
		log.Error("failed to parse NODE_ID", "err", err)
		os.Exit(1)
	}

	currentBranch := os.Getenv("GITHUB_REF")

	log.Info("start", "branch", currentBranch, "node", currentNode, "max_nodes", maxNodes, "input_files_count", len(inputFiles))

	ghClient := github.New(github.Opts{
		Token: os.Getenv("GITHUB_TOKEN"),
		Repo:  os.Getenv("GITHUB_REPOSITORY"),
	}, log.New("module", "github"))

	// Get up to 500 artifacts (5 pages of 100 items)
	artifacts, err := ghClient.ListArtifacts(ctx)
	if err != nil {
		log.Error("failed to list artifacts", "err", err)
		os.Exit(1)
	}

	// Two attempts to get artifacts: from main branch (stable artifacts) and from current branch (repeat runs)
	// TODO: get it only from main branch
	for i := 0; i < 2; i++ {
		filteredArtifacts := make([]github.Artifact, 0)
		for _, artifact := range artifacts {
			if artifact.Expired {
				continue
			}
			if artifact.Name != "test-results" {
				continue
			}

			if i == 0 && artifact.WorkflowRun.HeadBranch == "main" || i == 1 && artifact.WorkflowRun.HeadBranch == currentBranch {
				filteredArtifacts = append(filteredArtifacts, artifact)
			}
		}

		if len(filteredArtifacts) > 0 {
			artifacts = filteredArtifacts
			break
		}
	}

	// Sort artifacts by created_at (newest first)
	sort.Slice(artifacts, func(i, j int) bool {
		createdAtLeft, _ := time.Parse(dtFormat, artifacts[i].CreatedAt)
		createdAtRight, _ := time.Parse(dtFormat, artifacts[j].CreatedAt)

		return createdAtLeft.After(createdAtRight)
	})

	// Download artifacts if we found them and put into a map to avoid duplicates in case artifacts were from builds
	// with different nodes count
	tests := make(map[string]Tests)
	for _, artifact := range artifacts {
		log.Info("download artifact", "id", artifact.ID, "name", artifact.Name, "branch", artifact.WorkflowRun.HeadBranch, "created_at", artifact.CreatedAt)
		zipContent, err := ghClient.DownloadArtifact(ctx, artifact.ID)
		if err != nil {
			log.Error("failed to download artifact", "err", err)
			break
		}

		zipReader, err := zip.NewReader(bytes.NewReader(zipContent), int64(len(zipContent)))
		if err != nil {
			log.Error("failed to open zip archive", "err", err)
			break
		}

		if len(zipReader.File) == 0 {
			log.Error("zip archive is empty")
			break
		}

		for _, file := range zipReader.File {
			if !strings.HasPrefix(file.Name, "rspec-") || !strings.HasSuffix(file.Name, ".xml") {
				continue
			}

			log.Info("process file", "name", file.Name)
			f, err := file.Open()
			if err != nil {
				log.Error("failed to open zip file", "err", err)
				break
			}
			defer f.Close()

			xmlContent, err := io.ReadAll(f)
			if err != nil {
				log.Error("failed to read zip file", "err", err)
				break
			}

			if len(xmlContent) == 0 {
				log.Error("empty rspec.xml file found")
				break
			}

			var rspec RSpec
			err = xml.Unmarshal(xmlContent, &rspec)
			if err != nil {
				log.Error("failed to unmarshal xml", "err", err)
				break
			}

			for _, testCase := range rspec.TestCases {
				tests[testCase.File+"/"+testCase.Name] = Tests{
					Filename: testCase.File,
					Name:     testCase.Name,
					Time:     testCase.Time,
				}
			}
		}

		break // that's correct, we need only one artifact
	}

	log.Info("found tests in artifacts", "count", len(tests))

	// sum timing per file (because one file have multiple tests)
	filesMap := make(map[string]float64)
	allTime := 0.0
	for _, test := range tests {
		filesMap[test.Filename] += test.Time
		allTime += test.Time
	}

	avgFileTime := 0.0
	if len(filesMap) > 0 {
		avgFileTime = allTime / float64(len(filesMap))
	}

	// remove disappeared files from input
	removeCandidates := make([]string, 0)
	for filename := range filesMap {
		found := false
		for _, line := range inputFiles {
			if line == filename {
				found = true
				break
			}
		}

		if !found {
			removeCandidates = append(removeCandidates, filename)
		}
	}

	for _, filename := range removeCandidates {
		log.Debug("file was found in artifacts but not mentioned in input", "filename", filename)
		delete(filesMap, filename)
	}

	// add new files from input with average time
	for _, line := range inputFiles {
		if _, ok := filesMap[line]; !ok {
			log.Debug("file weren't found in artifacts but mentioned in input", "filename", line)
			filesMap[line] = avgFileTime
		}
	}

	// sort files to have a deterministic order
	type tmp struct {
		filename string
		time     float64
	}
	filesMapSorted := make([]tmp, 0)
	for filename, time := range filesMap {
		filesMapSorted = append(filesMapSorted, tmp{filename: filename, time: time})
	}

	sort.Slice(filesMapSorted, func(i, j int) bool {
		return filesMapSorted[i].filename < filesMapSorted[j].filename
	})

	log.Info("files to run", "count", len(filesMapSorted))

	// create N chunks and put files into them
	chunks := make(map[int][]string)
	timing := make(map[int]float64)
	for i := 0; i < maxNodes; i++ {
		chunks[i] = make([]string, 0)
		timing[i] = 0.0
	}

	for _, item := range filesMapSorted {
		min := 100000000.0
		chunk := -1
		for i := 0; i < maxNodes; i++ {
			if timing[i] < min {
				min = timing[i]
				chunk = i
			}
		}

		chunks[chunk] = append(chunks[chunk], item.filename)
		timing[chunk] += item.time
	}

	// print chunks stats
	for i := 0; i < maxNodes; i++ {
		log.Debug("chunk", "index", i, "count", len(chunks[i]), "time", timing[i])
	}

	log.Info("return chunk for NODE_ID", "index", currentNode)
	chunkFiles, ok := chunks[currentNode]
	if !ok {
		log.Error("failed to get chunk for NODE_ID", "index", currentNode)
		return
	}

	f := os.Stdout
	writer := bufio.NewWriter(f)

	for _, filename := range chunkFiles {
		log.Info("file to run", "filename", filename)
		writer.WriteString(filename + "\n")
	}

	writer.Flush()
}
