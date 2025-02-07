// Copyright (c) 2021 Palantir Technologies. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package crawl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/fatih/color"
)

type Reporter struct {
	// if non-nil, reported output is written to this writer
	OutputWriter io.Writer
	// True if reported output should be JSON, false otherwise
	OutputJSON bool
	// True if the reported output should consist of only the path to the file with the CVE, false otherwise. Only has
	// an effect if OutputJSON is false.
	OutputFilePathOnly bool
	// Disables results only matching JndiLookup classes
	DisableFlaggingJndiLookup bool
	// Disables detection of CVE-2021-45105
	DisableCVE45105 bool
	// Disables detection of CVE-2021-44832
	DisableCVE44832 bool
	// Disables flagging issues where version of log4j is not known
	DisableFlaggingUnknownVersions bool
	// Number of issues that have been found
	count int64
}

type JavaCVEInstance struct {
	Message       string   `json:"message"`
	FilePath      string   `json:"filePath"`
	CVEsDetected  []string `json:"cvesDetected"`
	Findings      []string `json:"findings"`
	Log4JVersions []string `json:"log4jVersions"`
}

var Log4jVersionRegex = regexp.MustCompile(`(?i)^(\d+)\.(\d+)\.?(\d+)?(?:[\./-].*)?$`)

type Log4jVersion struct {
	Major int
	Minor int
	Patch int
}

type AffectedVersion struct {
	CVE             string
	FixedAfter      Log4jVersion
	PatchedVersions []Log4jVersion
}

var cveVersions = []AffectedVersion{
	{
		CVE: "CVE-2021-44228",
		FixedAfter: Log4jVersion{
			Major: 2,
			Minor: 16,
			Patch: 0,
		},
		PatchedVersions: []Log4jVersion{
			{
				Major: 2,
				Minor: 12,
				Patch: 2,
			},
			{
				Major: 2,
				Minor: 3,
				Patch: 1,
			},
		},
	},
	{
		CVE: "CVE-2021-45046",
		FixedAfter: Log4jVersion{
			Major: 2,
			Minor: 16,
			Patch: 0,
		},
		PatchedVersions: []Log4jVersion{
			{
				Major: 2,
				Minor: 12,
				Patch: 2,
			},
			{
				Major: 2,
				Minor: 3,
				Patch: 1,
			},
		},
	},
	{
		CVE: "CVE-2021-45105",
		FixedAfter: Log4jVersion{
			Major: 2,
			Minor: 17,
			Patch: 0,
		},
		PatchedVersions: []Log4jVersion{
			{
				Major: 2,
				Minor: 12,
				Patch: 3,
			},
			{
				Major: 2,
				Minor: 3,
				Patch: 1,
			},
		},
	},
	{
		CVE: "CVE-2021-44832",
		FixedAfter: Log4jVersion{
			Major: 2,
			Minor: 17,
			Patch: 1,
		},
		PatchedVersions: []Log4jVersion{
			{
				Major: 2,
				Minor: 12,
				Patch: 4,
			},
			{
				Major: 2,
				Minor: 3,
				Patch: 2,
			},
		},
	},
}

// Collect increments the count of number of calls to Reporter.Collect and logs the path of the vulnerable file to disk.
func (r *Reporter) Collect(ctx context.Context, path string, d fs.DirEntry, result Finding, versionSet Versions) {
	versions := sortVersions(versionSet)
	if r.DisableFlaggingUnknownVersions && (len(versions) == 0 || len(versions) == 1 && versions[0] == UnknownVersion) {
		return
	}
	cvesFound := r.matchedCVEs(versions)
	if len(cvesFound) == 0 {
		return
	}
	if r.DisableFlaggingJndiLookup && jndiLookupResultsOnly(result) {
		return
	}
	r.count++

	// if no output writer is specified, nothing more to do
	if r.OutputWriter == nil {
		return
	}

	cveMessage := strings.Join(cvesFound, ", ") + " detected"

	var readableReasons []string
	var findingNames []string
	if result&JndiLookupClassName > 0 && !r.DisableFlaggingJndiLookup {
		readableReasons = append(readableReasons, "JndiLookup class name matched")
		findingNames = append(findingNames, "jndiLookupClassName")
	}
	if result&JndiLookupClassPackageAndName > 0 && !r.DisableFlaggingJndiLookup {
		readableReasons = append(readableReasons, "JndiLookup class and package name matched")
		findingNames = append(findingNames, "jndiLookupClassPackageAndName")
	}
	if result&JndiManagerClassName > 0 {
		readableReasons = append(readableReasons, "JndiManager class name matched")
		findingNames = append(findingNames, "jndiManagerClassName")
	}
	if result&JarName > 0 {
		readableReasons = append(readableReasons, "jar name matched")
		findingNames = append(findingNames, "jarName")
	}
	if result&JarNameInsideArchive > 0 {
		readableReasons = append(readableReasons, "jar name inside archive matched")
		findingNames = append(findingNames, "jarNameInsideArchive")
	}
	if result&JndiManagerClassPackageAndName > 0 {
		readableReasons = append(readableReasons, "JndiManager class and package name matched")
		findingNames = append(findingNames, "jndiManagerClassPackageAndName")
	}
	if result&ClassFileMd5 > 0 {
		readableReasons = append(readableReasons, "class file MD5 matched")
		findingNames = append(findingNames, "classFileMd5")
	}
	if result&ClassBytecodeInstructionMd5 > 0 {
		readableReasons = append(readableReasons, "byte code instruction MD5 matched")
		findingNames = append(findingNames, "classBytecodeInstructionMd5")
	}
	if result&JarFileObfuscated > 0 {
		readableReasons = append(readableReasons, "jar file appeared obfuscated")
		findingNames = append(findingNames, "jarFileObfuscated")
	}
	if result&ClassBytecodePartialMatch > 0 {
		readableReasons = append(readableReasons, "byte code partially matched known version")
		findingNames = append(findingNames, "classBytecodePartialMatch")
	}

	var outputToWrite string
	if r.OutputJSON {
		cveInfo := JavaCVEInstance{
			Message:       cveMessage,
			FilePath:      path,
			CVEsDetected:  cvesFound,
			Findings:      findingNames,
			Log4JVersions: versions,
		}
		// should not fail
		jsonBytes, _ := json.Marshal(cveInfo)
		outputToWrite = string(jsonBytes)
	} else if r.OutputFilePathOnly {
		outputToWrite = path
	} else {
		outputToWrite = color.YellowString("[MATCH] "+cveMessage+" in file %s. log4j versions: %s. Reasons: %s", path, strings.Join(versions, ", "), strings.Join(readableReasons, ", "))
	}
	_, _ = fmt.Fprintln(r.OutputWriter, outputToWrite)
}

func (r *Reporter) matchedCVEs(versions []string) []string {
	if len(versions) == 0 {
		return []string{"unknown version - unknown CVE status"}
	}
	cvesFound := make(map[string]struct{})
	for _, version := range versions {
		major, minor, patch, parsed := ParseLog4jVersion(version)
		if !parsed {
			cvesFound["invalid version - unknown CVE status"] = struct{}{}
		}
		for _, vulnerability := range cveVersions {
			if major >= vulnerability.FixedAfter.Major && minor >= vulnerability.FixedAfter.Minor && patch >= vulnerability.FixedAfter.Patch {
				continue
			}
			vulnerable := true
			for _, fixedVersion := range vulnerability.PatchedVersions {
				if major == fixedVersion.Major && minor == fixedVersion.Minor && patch >= fixedVersion.Patch {
					vulnerable = false
					break
				}
			}
			if vulnerable && !(r.DisableCVE45105 && vulnerability.CVE == "CVE-2021-45105") && !(r.DisableCVE44832 && vulnerability.CVE == "CVE-2021-44832") {
				cvesFound[vulnerability.CVE] = struct{}{}
			}
		}
	}
	var uniqueCVEs []string
	for cve := range cvesFound {
		uniqueCVEs = append(uniqueCVEs, cve)
	}
	sort.Strings(uniqueCVEs)
	return uniqueCVEs
}

func jndiLookupResultsOnly(result Finding) bool {
	return result == JndiLookupClassName || result == JndiLookupClassPackageAndName
}

func sortVersions(versions Versions) []string {
	var out []string
	for v := range versions {
		out = append(out, v)
	}
	// N.B. Lexical sort will mess with base-10 versions, but it's better than random.
	sort.Strings(out)
	return out
}

// Count returns the number of times that Collect has been called
func (r Reporter) Count() int64 {
	return r.count
}

func ParseLog4jVersion(version string) (int, int, int, bool) {
	matches := Log4jVersionRegex.FindStringSubmatch(version)
	if len(matches) == 0 {
		return 0, 0, 0, false
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		// should not be possible due to group of \d+ in regex
		return 0, 0, 0, false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		// should not be possible due to group of \d+ in regex
		return 0, 0, 0, false
	}
	patch, err := strconv.Atoi(matches[3])
	if err != nil {
		patch = 0
	}
	return major, minor, patch, true
}
