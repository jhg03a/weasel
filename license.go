/*
Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements.  See the NOTICE file
distributed with this work for additional information
regarding copyright ownership.  The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License.  You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied.  See the License for the
specific language governing permissions and limitations
under the License.
*/
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func main() {
	quiet := false
	cd := ``
	argDone := false
	for _, arg := range os.Args[1:] {
		if !argDone {
			if arg == `-q` {
				quiet = true
				continue
			}
			if arg == `--` {
				argDone = true
				continue
			}
		}
		if cd == `` {
			cd = arg
			continue
		}
		fmt.Println("Unknown argument: `" + arg + "`!")
		return
	}
	if cd == `` {
		/* Find the .git directory. */
		p, err := os.Getwd()
		if err != nil {
			fmt.Println("Unable to get working directory: " + err.Error())
			return
		}
		p = strings.TrimRight(p, `/`)

		patience := 10000 /* patience exists in case there are loops or other excessively long paths. */
		for p != `` && patience != 0 {
			if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && fi.IsDir() {
				cd = p
				break
			}
			p, _ = filepath.Split(p)
			p = strings.TrimRight(p, `/`)

			patience--
		}
	}
	fmt.Println("Using directory: " + cd)
	err := os.Chdir(cd)
	if err != nil {
		fmt.Println("Failed to enter target directory: " + err.Error() + "!")
		return
	}

	recordDocumentedLicenses()

	files := make(map[string][]License)
	var wg sync.WaitGroup
	var filesLock sync.Mutex
	err = filepath.Walk(`.`, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if filepath.Base(name) == `.git` {
			return filepath.SkipDir
		}

		if Ignored(name) {
			return nil
		}

		if info.IsDir() {
			return nil
		}

		if (info.Mode() & os.ModeSymlink) != 0 {
			return nil
		}

		if info.Size() == 0 {
			filesLock.Lock()
			defer filesLock.Unlock()
			files[name] = append(files[name], License("Empty"))
			return nil
		}

		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			licenses, err := fileLicenses(name)
			if err != nil {
				licenses = []License{License("Error: " + err.Error() + "!")}
			}

			filesLock.Lock()
			defer filesLock.Unlock()
			files[name] = append(files[name], override[name]...)
			files[name] = append(files[name], licenses...)
			files[name] = Collide(Uniq(files[name]))
		}(name)
		return nil
	})
	wg.Wait()
	if err != nil {
		fmt.Println(err)
		return
	}

forUnknownFiles:
	for name, licenses := range files {
		if len(licenses) == 0 {
			parts := strings.Split(name, `/`)
			for i := len(parts) - 1; i > 0; i-- {
				for _, licName := range []string{`LICENSE`, `LICENCE`, `LICENSE.md`, `LICENCE.md`, `LICENSE.txt`, `LICENCE.txt`} {
					licPath := strings.Join(parts[:i], `/`) + `/` + licName
					if len(files[licPath]) != 0 {
						for _, license := range files[licPath] {
							if license != License(`Docs`) {
								files[name] = append(files[name], License(string(license)+"~"))
							}
						}
						continue forUnknownFiles
					}
				}
			}
		}
	}

	for name, licenses := range files {
		if len(licenses) != 0 {
			if len(licenses) > 1 || (licenses[0] != License(`Apache`) && licenses[0] != License(`Docs`) && licenses[0] != License(`Empty`) && licenses[0] != License(`Ignore`)) {
				if !documented.Documents(name) {
					for i, lic := range licenses {
						if lic != License(`Apache`) && lic != License(`Docs`) && lic != License(`Empty`) && lic != License(`Ignore`) {
							licenses[i] = License(string(licenses[i]) + `!`)
						}
					}
				}
			}
		}
	}

	for name, licenses := range files {
		if len(licenses) == 0 {
			kind := filekind(name)
			if kind != `` {
				files[name] = []License{License(kind)}
			}
		}
	}

	var filenames []string
	for filename := range files {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)

	failed := false
	for _, filename := range filenames {
		lics := files[filename]
		ignore := false
		undoc := false
		var licStr string
		if len(lics) == 0 {
			licStr = "Unknown!"
			undoc = true
		} else {
			licStr = fmt.Sprint(lics[0])
			ignore = (licStr == `Ignore`)
			if len(licStr) > 0 && licStr[len(licStr)-1] == '!' {
				undoc = true
			}
			for _, lic := range lics[1:] {
				if string(lic) == `Ignore` {
					ignore = true
				}
				licStr = licStr + `, ` + fmt.Sprint(lic)
			}
		}
		if !ignore {
			errStr := ""
			if undoc {
				errStr = "Error"
				failed = true
			}
			if undoc || !quiet {
				fmt.Printf("%-6s%40s %s\n", errStr, licStr, filename)
			}
		}
	}
	for _, extra := range documented.Extra() {
		fmt.Printf("%-6s%40s %s\n", "Error", "Extra-License!", extra)
		failed = true
	}

	if failed {
		os.Exit(1)
	}
	os.Exit(0)
}

func fileLicenses(name string) ([]License, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	return identifyLicenses(f)
}

func identifyLicenses(in io.Reader) ([]License, error) {

	ch := make(chan string, 32)
	go func() {
		s := bufio.NewScanner(in)
		s.Split(bufio.ScanWords)
		for s.Scan() {
			s := strings.ToLower(stripPunc(s.Text()))
			if len(s) > 0 {
				ch <- s
			}
		}
		close(ch)
	}()

	licenses := newMultiMatcher(ch)
	return licenses, nil
}