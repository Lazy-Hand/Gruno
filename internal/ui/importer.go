package ui

// importer.go imports an exported collection (Postman for now) as a new
// Bruno collection directory: the file is parsed by internal/importer,
// then written with the same store calls the app uses everywhere.

import (
	"fmt"
	"os"
	"strings"

	"github.com/egoist/mygo"

	"gruno/bru"
	"gruno/internal/importer"
)

// importPostman imports a Postman export as a new collection.
func (a *app) importPostman() {
	a.importFile("选择 Postman 集合文件", []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}}, importer.ParsePostman)
}

// importOpenAPI imports an OpenAPI 3.x document as a new collection.
func (a *app) importOpenAPI() {
	a.importFile("选择 OpenAPI 文档", []mygo.FileFilter{{Name: "OpenAPI", Extensions: []string{"json", "yaml", "yml"}}}, importer.ParseOpenAPI)
}

// importFile asks for a file, parses it with parse, asks for a directory
// for the new collection, writes it and opens it.
func (a *app) importFile(title string, filters []mygo.FileFilter, parse func([]byte) (*importer.Collection, error)) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title, Filters: filters})
	if err != nil || len(paths) == 0 {
		return
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		mygo.Dialog.Error("读取失败", err.Error())
		return
	}
	imp, err := parse(data)
	if err != nil {
		mygo.Dialog.Error("导入失败", err.Error())
		return
	}
	dest, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "选择新集合的目录位置"})
	if err != nil || dest == "" {
		return
	}
	c, err := CreateCollection(dest, imp.Name)
	if err != nil {
		mygo.Dialog.Error("创建集合失败", err.Error())
		return
	}
	if err := writeImport(c, imp); err != nil {
		mygo.Dialog.Error("导入失败", err.Error())
		return
	}
	a.openCollection(dest)
	if a.openErr == "" && len(imp.Warnings) > 0 {
		mygo.Dialog.Error(fmt.Sprintf("已导入，%d 项未导入", len(imp.Warnings)), warningText(imp.Warnings))
	}
}

// warningText lists up to ten skipped items.
func warningText(ws []string) string {
	const max = 10
	if len(ws) <= max {
		return strings.Join(ws, "\n")
	}
	return strings.Join(ws[:max], "\n") + fmt.Sprintf("\n…另外 %d 项", len(ws)-max)
}

// writeImport writes the imported collection into c, which must be new.
func writeImport(c *Collection, imp *importer.Collection) error {
	if imp.Bru != nil {
		c.Dot = imp.Bru
		if err := c.SaveCollectionBru(); err != nil {
			return err
		}
	}
	return writeImportLevel(c, "", imp.Folders, imp.Requests)
}

func writeImportLevel(c *Collection, parent string, folders []*importer.Folder, reqs []*bru.Request) error {
	for _, f := range folders {
		nf, err := c.AddFolder(parent, f.Name)
		if err != nil {
			return err
		}
		if f.Bru != nil {
			nf.Bru = f.Bru
			if err := c.SaveFolderBru(nf); err != nil {
				return err
			}
		}
		if err := writeImportLevel(c, nf.Path, f.Folders, f.Requests); err != nil {
			return err
		}
	}
	for _, r := range reqs {
		br, err := c.AddRequest(parent, r.Meta.Name)
		if err != nil {
			return err
		}
		br.Req = r
		if err := c.SaveRequest(br); err != nil {
			return err
		}
	}
	return nil
}
