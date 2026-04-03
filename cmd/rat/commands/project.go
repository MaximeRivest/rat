package commands

import "github.com/maximerivest/rat/internal/project"

func findProjectRoot(dir string) (root string, found bool) {
	return project.FindRoot(dir)
}

func projectName(root string) string {
	return project.Name(root)
}

func findVenv(dir string) string {
	return project.FindVenv(dir)
}
