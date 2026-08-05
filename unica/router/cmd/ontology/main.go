// Command ontology authors, validates and publishes per-product-line domain
// ontologies.
//
// UNICA is delivered to many customers, each with their own facts, so an
// ontology is data rather than code: this tool is how a YAML file written by an
// operations person becomes the active version the router reads. Every
// subcommand except `validate` needs POSTGRES_URL; `validate` deliberately does
// not, so an author can check their work without access to a deployment.
//
//	go run ./cmd/ontology validate -dir ../../deploy/config/ontology
//	go run ./cmd/ontology preview  -file ../../deploy/config/ontology/techzone.yaml
//	go run ./cmd/ontology publish  -dir ../../deploy/config/ontology
//	go run ./cmd/ontology versions -line TechZone
//	go run ./cmd/ontology rollback -line TechZone -version 2
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/pkg/domain"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "preview":
		err = cmdPreview(os.Args[2:])
	case "publish":
		err = cmdPublish(os.Args[2:])
	case "versions":
		err = cmdVersions(os.Args[2:])
	case "rollback":
		err = cmdRollback(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ontology: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ontology <subcommand> [flags]

  validate  -dir D | -file F        parse and check ontologies; no database needed
  preview   -dir D | -file F        print the facts block that would be injected
  publish   -dir D | -file F        store as a new active version (needs POSTGRES_URL)
  versions  -line NAME              list stored revisions
  rollback  -line NAME -version N   reactivate an earlier revision
`)
}

// load reads ontologies from either a single file or a directory, keeping the
// source text so publish can retain it verbatim.
func load(dir, file string) ([]*domain.Ontology, map[string]string, error) {
	if (dir == "") == (file == "") {
		return nil, nil, errors.New("provide exactly one of -dir or -file")
	}

	sources := make(map[string]string)

	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", file, err)
		}
		o, err := domain.ParseYAML(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
		sources[o.ProductLine] = string(raw)
		return []*domain.Ontology{o}, sources, nil
	}

	sets, err := domain.LoadYAMLDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range sets {
		raw, err := os.ReadFile(pathFor(dir, o.ProductLine))
		if err == nil {
			sources[o.ProductLine] = string(raw)
		}
	}
	return sets, sources, nil
}

// pathFor re-derives a source path by scanning the directory, because the file
// name need not match the product line inside it.
func pathFor(dir, productLine string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := dir + string(os.PathSeparator) + e.Name()
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if o, err := domain.ParseYAML(raw); err == nil && o.ProductLine == productLine {
			return path
		}
	}
	return ""
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	dir := fs.String("dir", "", "directory of ontology YAML files")
	file := fs.String("file", "", "single ontology YAML file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sets, _, err := load(*dir, *file)
	if err != nil {
		return err
	}

	for _, o := range sets {
		facts := 0
		for _, name := range o.PropertyNames() {
			facts += len(o.FactsOf(name))
		}
		conditional := 0
		for _, name := range o.PropertyNames() {
			if len(o.DistinctFactsOf(name)) > 1 {
				conditional++
			}
		}
		fmt.Printf("OK  %-12s v%d  %d 类 / %d 维度 / %d 属性 / %d 断言 / %d 条否定  (其中 %d 项按情形分叉)\n",
			o.ProductLine, o.Version, len(o.Classes), len(o.Dimensions),
			len(o.Properties), facts, len(o.Denials), conditional)
	}
	return nil
}

func cmdPreview(args []string) error {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	dir := fs.String("dir", "", "directory of ontology YAML files")
	file := fs.String("file", "", "single ontology YAML file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sets, _, err := load(*dir, *file)
	if err != nil {
		return err
	}

	for i, o := range sets {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("=== %s ===\n%s", o.ProductLine, domain.Render(o))
	}
	return nil
}

func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ExitOnError)
	dir := fs.String("dir", "", "directory of ontology YAML files")
	file := fs.String("file", "", "single ontology YAML file")
	note := fs.String("note", "", "change note stored with the version")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sets, sources, err := load(*dir, *file)
	if err != nil {
		return err
	}

	db, err := open()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store := domain.NewStore(db, 0)
	for _, o := range sets {
		id, err := productLineID(ctx, db, o.ProductLine)
		if err != nil {
			return err
		}
		version, err := store.Publish(ctx, id, o, sources[o.ProductLine], *note)
		if err != nil {
			return fmt.Errorf("%s: %w", o.ProductLine, err)
		}
		fmt.Printf("published %s v%d\n", o.ProductLine, version)
	}
	return nil
}

func cmdVersions(args []string) error {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	line := fs.String("line", "", "product line name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *line == "" {
		return errors.New("-line is required")
	}

	db, err := open()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id, err := productLineID(ctx, db, *line)
	if err != nil {
		return err
	}

	versions, err := domain.NewStore(db, 0).Versions(ctx, id)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		fmt.Printf("%s has no ontology yet\n", *line)
		return nil
	}

	for _, v := range versions {
		marker := " "
		if v.Active {
			marker = "*"
		}
		fmt.Printf("%s v%-3d %s  %s\n", marker, v.Version,
			v.CreatedAt.Format("2006-01-02 15:04"), v.Note)
	}
	return nil
}

func cmdRollback(args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	line := fs.String("line", "", "product line name")
	version := fs.Int("version", 0, "version to reactivate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *line == "" || *version <= 0 {
		return errors.New("-line and -version are required")
	}

	db, err := open()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	id, err := productLineID(ctx, db, *line)
	if err != nil {
		return err
	}
	if err := domain.NewStore(db, 0).Rollback(ctx, id, *version); err != nil {
		return err
	}

	fmt.Printf("rolled %s back to v%d\n", *line, *version)
	return nil
}

func open() (*sql.DB, error) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		return nil, errors.New("POSTGRES_URL is not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

func productLineID(ctx context.Context, db *sql.DB, name string) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM product_lines WHERE name = $1`, name).Scan(&id)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("product line %q not found; the ontology's product_line "+
			"must match a row in product_lines", name)
	}
	if err != nil {
		return "", fmt.Errorf("look up product line %q: %w", name, err)
	}
	return id, nil
}
