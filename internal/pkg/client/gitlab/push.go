// -----------------------------------------------------------------------------
// SPDX-FileCopyrightText: Copyright © 2024 bomctl a Series of LF Projects, LLC
// SPDX-FileName: internal/pkg/client/gitlab/push.go
// SPDX-FileType: SOURCE
// SPDX-License-Identifier: Apache-2.0
// -----------------------------------------------------------------------------
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// -----------------------------------------------------------------------------

package gitlab

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"

	"github.com/bomctl/bomctl/internal/pkg/db"
	"github.com/bomctl/bomctl/internal/pkg/options"
	"github.com/bomctl/bomctl/internal/pkg/outpututil"
)

type StringWriter struct {
	*strings.Builder
}

func (*StringWriter) Close() error {
	return nil
}

func (client *Client) PreparePush(pushURL string, _opts *options.PushOptions) error {
	gitLabToken := os.Getenv("BOMCTL_GITLAB_TOKEN")

	url := client.Parse(pushURL)

	host := url.Hostname

	if url.Port != "" {
		host = fmt.Sprintf("%s:%s", host, url.Port)
	}

	baseURL := fmt.Sprintf("https://%s/api/v4", host)

	gitLabClient, err := gitlab.NewClient(gitLabToken, gitlab.WithBaseURL(baseURL))
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	client.GitLabToken = gitLabToken
	client.ProjectProvider = gitLabClient.Projects
	client.GenericPackagePublisher = gitLabClient.GenericPackages

	client.PushQueue = make([]*SbomFile, 0)

	return nil
}

func (client *Client) AddFile(_pushURL, id string, opts *options.PushOptions) error {
	opts.Logger.Info("Adding file", "id", id)

	backend, err := db.BackendFromContext(opts.Context())
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	sbom, err := backend.GetDocumentByIDOrAlias(id)
	if err != nil {
		return fmt.Errorf("%w", err)
	}

	uuidRegex := regexp.MustCompile(`urn:uuid:([\w-]+)`)
	uuidMatch := uuidRegex.FindStringSubmatch(id)
	sbomFilename := uuidMatch[1]

	xmlFormatRegex := regexp.MustCompile(`\bxml\b`)
	xmlFormatMatch := xmlFormatRegex.FindStringSubmatch(string(opts.Format))

	if len(xmlFormatMatch) == 0 {
		sbomFilename += ".json"
	} else {
		sbomFilename += ".xml"
	}

	sbomWriter := &StringWriter{&strings.Builder{}}
	if err := outpututil.WriteStream(sbom, opts.Format, opts.Options, sbomWriter); err != nil {
		return fmt.Errorf("failed to serialize SBOM %s: %w", id, err)
	}

	client.PushQueue = append(client.PushQueue, &SbomFile{
		Name:     sbomFilename,
		Contents: sbomWriter.String(),
	})

	return nil
}

func (client *Client) Push(pushURL string, _opts *options.PushOptions) error {
	url := client.Parse(pushURL)
	if url == nil {
		return fmt.Errorf("%w: %s", errInvalidGitLabURL, pushURL)
	}

	project, response, err := client.GetProject(url.Path, nil)
	if err != nil {
		return fmt.Errorf("failed to get project info: %w", err)
	}

	if err := validateHTTPStatusCode(response.StatusCode); err != nil {
		return err
	}

	packageName := ""
	packageVersion := ""

	parameters := strings.Split(url.Query, "&")

	for _, parameter := range parameters {
		nameValuePair := strings.Split(parameter, "=")

		switch nameValuePair[0] {
		case "package_name":
			packageName = nameValuePair[1]
		case "package_version":
			packageVersion = nameValuePair[1]
		}
	}

	for _, sbomFile := range client.PushQueue {
		sbomReader := strings.NewReader(sbomFile.Contents)

		_, response, err := client.GenericPackagePublisher.PublishPackageFile(
			project.ID,
			packageName,
			packageVersion,
			sbomFile.Name,
			sbomReader,
			nil,
		)
		if err != nil {
			return fmt.Errorf("failed to push sbom: %w", err)
		}

		if err := validateHTTPStatusCode(response.StatusCode); err != nil {
			return err
		}
	}

	return nil
}
