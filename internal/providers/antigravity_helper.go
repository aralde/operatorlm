package providers

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type DiscoveredProject struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// GetDiscoveredProjects returns a list of discovered valid projects and their paths.
func GetDiscoveredProjects() []DiscoveredProject {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	pbPath := filepath.Join(home, ".gemini", "antigravity", "agyhub_summaries_proto.pb")
	data, err := os.ReadFile(pbPath)
	if err != nil {
		return nil
	}

	uuidRegex := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	uuidMatches := uuidRegex.FindAllIndex(data, -1)

	uriRegex := regexp.MustCompile(`file:///[^\s\x00-\x1f\x7f-\xff]+`)
	uriMatches := uriRegex.FindAllIndex(data, -1)

	var result []DiscoveredProject
	seen := make(map[string]bool)

	for _, uriMatch := range uriMatches {
		uriStr := string(data[uriMatch[0]:uriMatch[1]])
		uriStr = strings.Split(uriStr, "\x0a")[0]
		uriStr = strings.Split(uriStr, "\x12")[0]
		uriStr = strings.Split(uriStr, "\x1a")[0]

		var bestUUID string
		minDist := 999999
		for _, uuidMatch := range uuidMatches {
			dist := uuidMatch[0] - uriMatch[1]
			if dist >= 0 && dist < minDist && dist < 300 {
				minDist = dist
				bestUUID = string(data[uuidMatch[0]:uuidMatch[1]])
			}
		}

		if bestUUID != "" {
			cleanPath := cleanURI(uriStr)
			if cleanPath != "" {
				if _, err := os.Stat(cleanPath); err == nil {
					if !seen[bestUUID] {
						seen[bestUUID] = true
						result = append(result, DiscoveredProject{
							ID:   bestUUID,
							Path: cleanPath,
						})
					}
				}
			}
		}
	}
	return result
}

// getProjectIDFromPB attempts to discover a valid project ID from the local Antigravity
// agyhub_summaries_proto.pb file. It prefers the project ID matching the current working
// directory (CWD) or falls back to the most recently active project ID.
func getProjectIDFromPB() string {
	projectID := os.Getenv("ANTIGRAVITY_PROJECT_ID")
	if projectID != "" {
		return projectID
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	pbPath := filepath.Join(home, ".gemini", "antigravity", "agyhub_summaries_proto.pb")
	data, err := os.ReadFile(pbPath)
	if err != nil {
		return ""
	}

	uuidRegex := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	uuidMatches := uuidRegex.FindAllIndex(data, -1)

	uriRegex := regexp.MustCompile(`file:///[^\s\x00-\x1f\x7f-\xff]+`)
	uriMatches := uriRegex.FindAllIndex(data, -1)

	type projectMap struct {
		workspacePath string
		projectID     string
	}

	var projects []projectMap
	var fallbackProjects []projectMap
	for _, uriMatch := range uriMatches {
		uriStr := string(data[uriMatch[0]:uriMatch[1]])
		uriStr = strings.Split(uriStr, "\x0a")[0]
		uriStr = strings.Split(uriStr, "\x12")[0]
		uriStr = strings.Split(uriStr, "\x1a")[0]

		var bestUUID string
		minDist := 999999
		for _, uuidMatch := range uuidMatches {
			dist := uuidMatch[0] - uriMatch[1]
			if dist >= 0 && dist < minDist && dist < 300 {
				minDist = dist
				bestUUID = string(data[uuidMatch[0]:uuidMatch[1]])
			}
		}

		if bestUUID != "" {
			cleanPath := cleanURI(uriStr)
			if cleanPath != "" {
				fallbackProjects = append(fallbackProjects, projectMap{
					workspacePath: cleanPath,
					projectID:     bestUUID,
				})
				if _, err := os.Stat(cleanPath); err == nil {
					projects = append(projects, projectMap{
						workspacePath: cleanPath,
						projectID:     bestUUID,
					})
				}
			}
		}
	}

	if len(projects) == 0 {
		projects = fallbackProjects
	}

	// Try to match CWD to a workspace folder
	cwd, err := os.Getwd()
	if err == nil {
		cleanCWD := filepath.Clean(cwd)
		for i := len(projects) - 1; i >= 0; i-- {
			p := projects[i]
			if p.workspacePath == "" {
				continue
			}
			pPath := filepath.Clean(p.workspacePath)
			if strings.HasPrefix(strings.ToLower(cleanCWD), strings.ToLower(pPath)) ||
				strings.HasPrefix(strings.ToLower(pPath), strings.ToLower(cleanCWD)) {
				return p.projectID
			}
		}
	}

	// Fallback to the last project ID found (most recently active)
	if len(projects) > 0 {
		return projects[len(projects)-1].projectID
	}

	return ""
}

// cleanURI cleans the URI string from the protobuf binary dump and converts it to a filepath.
func cleanURI(uriStr string) string {
	var clean []rune
	for _, r := range uriStr {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '/' || r == ':' || r == '.' || r == '_' || r == '-' || r == '%' || r == '\\' {
			clean = append(clean, r)
		} else {
			break
		}
	}
	s := string(clean)

	unescaped, err := url.QueryUnescape(s)
	if err == nil {
		s = unescaped
	}

	if strings.HasPrefix(s, "file:///") {
		s = s[8:]
	} else if strings.HasPrefix(s, "file://") {
		s = s[7:]
	}

	return filepath.FromSlash(s)
}
