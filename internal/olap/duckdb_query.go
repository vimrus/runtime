//go:build duckdb

package olap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

type DuckDBQueryEngine struct {
	threads      int
	memoryLimit  string
	queryTimeout time.Duration
	maxRows      int
	querySlots   chan struct{}
}

func NewDuckDBQueryEngine(threads int, memoryLimit string, queryTimeout time.Duration, maxRows, maxConcurrentQueries int) (*DuckDBQueryEngine, error) {
	if threads <= 0 {
		threads = 4
	}
	if memoryLimit == "" {
		memoryLimit = "1GB"
	}
	if queryTimeout <= 0 {
		queryTimeout = 30 * time.Second
	}
	if maxRows <= 0 {
		maxRows = 10000
	}
	if maxConcurrentQueries <= 0 {
		maxConcurrentQueries = 4
	}
	return &DuckDBQueryEngine{
		threads:      threads,
		memoryLimit:  memoryLimit,
		queryTimeout: queryTimeout,
		maxRows:      maxRows,
		querySlots:   make(chan struct{}, maxConcurrentQueries),
	}, nil
}

func (e *DuckDBQueryEngine) Query(ctx context.Context, datasetRoot string, request QueryRequest) (QueryResult, error) {
	if request.SchemaVersion != QuerySchemaVersion {
		return QueryResult{}, fmt.Errorf("unsupported OLAP query schema version %d", request.SchemaVersion)
	}
	if err := ValidateOLAPQuery(request.SQL, 256*1024); err != nil {
		return QueryResult{}, err
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > MaxQueryOffset {
		return QueryResult{}, fmt.Errorf("OLAP query offset exceeds %d", MaxQueryOffset)
	}
	select {
	case e.querySlots <- struct{}{}:
		defer func() { <-e.querySlots }()
	case <-ctx.Done():
		return QueryResult{}, ctx.Err()
	}

	manifests, err := LoadManifests(datasetRoot)
	if err != nil {
		return QueryResult{}, err
	}
	if len(manifests) == 0 {
		return QueryResult{}, ErrDatasetUnavailable
	}

	queryCtx, cancel := context.WithTimeout(ctx, e.queryTimeout)
	defer cancel()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return QueryResult{}, err
	}
	defer db.Close()
	if err := configureDuckDB(queryCtx, db, e.memoryLimit, e.threads); err != nil {
		return QueryResult{}, err
	}
	if err := restrictDuckDBFilesystem(queryCtx, db, []string{datasetRoot}); err != nil {
		return QueryResult{}, err
	}
	if _, err := db.ExecContext(queryCtx, DuckDBCompatibilitySQL()); err != nil {
		return QueryResult{}, fmt.Errorf("load OLAP DuckDB compatibility macros: %w", err)
	}
	if err := createOLAPViews(queryCtx, db, datasetRoot, manifests); err != nil {
		return QueryResult{}, err
	}

	sqlText := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(request.SQL), ";"))
	maxRows := request.MaxRows
	if maxRows <= 0 || maxRows > e.maxRows {
		maxRows = e.maxRows
	}
	limitedSQL := fmt.Sprintf("SELECT * FROM (%s) AS olap_query LIMIT %d OFFSET %d", sqlText, maxRows+1, offset)
	rows, err := db.QueryContext(queryCtx, limitedSQL, request.Params...)
	if err != nil {
		return QueryResult{}, fmt.Errorf("execute OLAP query: %w", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	columnTypes := make(map[string]string, len(columns))
	typeRows, err := rows.ColumnTypes()
	if err != nil {
		return QueryResult{}, err
	}
	for _, column := range typeRows {
		columnTypes[column.Name()] = column.DatabaseTypeName()
	}
	resultRows := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return QueryResult{}, err
		}
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			if bytes, ok := values[index].([]byte); ok {
				row[column] = string(bytes)
			} else {
				row[column] = values[index]
			}
		}
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	truncated := len(resultRows) > maxRows
	if truncated {
		resultRows = resultRows[:maxRows]
	}

	var total int64
	if request.SkipCount {
		total = int64(offset + len(resultRows))
	} else {
		countSQL := fmt.Sprintf("SELECT COUNT(*) FROM (%s) AS olap_count", sqlText)
		if err := db.QueryRowContext(queryCtx, countSQL, request.Params...).Scan(&total); err != nil {
			return QueryResult{}, fmt.Errorf("count OLAP query rows: %w", err)
		}
	}
	generations := make(map[string]uint64, len(manifests))
	for table, manifest := range manifests {
		generations[table] = manifest.Generation
	}
	return QueryResult{
		SchemaVersion:        QuerySchemaVersion,
		CompatibilityVersion: SQLCompatibilityVersion,
		Columns:              columns,
		ColumnTypes:          columnTypes,
		Rows:                 resultRows,
		Total:                total,
		Generations:          generations,
		Truncated:            truncated,
	}, nil
}

// restrictDuckDBFilesystem limits external file access to the application's
// immutable OLAP dataset. Parquet views remain readable from allowed paths while
// user SQL cannot read arbitrary local files even if its syntax passes the
// conservative read-only validator.
func restrictDuckDBFilesystem(ctx context.Context, db *sql.DB, directories []string) error {
	allowed := make([]string, 0, len(directories))
	seen := make(map[string]struct{})
	for _, directory := range directories {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return fmt.Errorf("resolve OLAP filesystem directory: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		allowed = append(allowed, quoteSQLLiteral(absolute))
	}
	if len(allowed) == 0 {
		return errors.New("OLAP filesystem restrictions require at least one directory")
	}
	for _, statement := range []string{
		"SET allowed_directories=[" + strings.Join(allowed, ",") + "]",
		"SET enable_external_access=false",
		"SET lock_configuration=true",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("restrict OLAP DuckDB filesystem: %w", err)
		}
	}
	return nil
}

func createOLAPViews(ctx context.Context, db *sql.DB, datasetRoot string, manifests map[string]Manifest) error {
	for table, manifest := range manifests {
		paths := make([]string, 0)
		for _, parts := range manifest.Buckets {
			for _, item := range parts {
				path := filepath.Join(datasetRoot, filepath.FromSlash(item.Path))
				paths = append(paths, quoteSQLLiteral(path))
			}
		}
		if len(paths) == 0 {
			return fmt.Errorf("%w: OLAP table %s has no visible parts", ErrDatasetUnavailable, table)
		}
		statement := fmt.Sprintf(
			"CREATE VIEW %s AS SELECT * FROM read_parquet([%s], union_by_name=true)",
			quoteSQLIdentifier(table),
			strings.Join(paths, ","),
		)
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create OLAP view %s: %w", table, err)
		}
	}
	return createOLAPSyntheticViews(ctx, db, manifests)
}

func createOLAPSyntheticViews(ctx context.Context, db *sql.DB, manifests map[string]Manifest) error {
	type view struct {
		name      string
		dependsOn []string
		statement string
	}
	views := []view{
		{
			name:      "ztv_projectnotpl",
			dependsOn: []string{"zt_project"},
			statement: `CREATE VIEW "ztv_projectnotpl" AS SELECT * FROM "zt_project" WHERE COALESCE("deleted", 0) = 0 AND COALESCE("isTpl", 0) = 0`,
		},
		{
			name:      "ztv_tasknotpl",
			dependsOn: []string{"zt_task"},
			statement: `CREATE VIEW "ztv_tasknotpl" AS SELECT * FROM "zt_task" WHERE COALESCE("deleted", 0) = 0 AND COALESCE("isTpl", 0) = 0`,
		},
		{
			name:      "ztv_normalproduct",
			dependsOn: []string{"zt_product"},
			statement: `CREATE VIEW "ztv_normalproduct" AS SELECT * FROM "zt_product" WHERE COALESCE("shadow", 0) = 0`,
		},
		{
			name:      "ztv_projectteams",
			dependsOn: []string{"zt_team"},
			statement: `CREATE VIEW "ztv_projectteams" AS SELECT "root" AS "execution", COUNT(*) AS "teams" FROM "zt_team" WHERE "type" = 'execution' GROUP BY "root"`,
		},
		{
			name:      "ztv_projectstories",
			dependsOn: []string{"zt_projectstory", "zt_story", "zt_project"},
			statement: `CREATE VIEW "ztv_projectstories" AS
				SELECT ps."project" AS "execution", COUNT(*) AS "stories", SUM(CASE WHEN s."status" = 'closed' THEN 0 ELSE 1 END) AS "undone"
				FROM "zt_projectstory" ps
				LEFT JOIN "zt_story" s ON ps."story" = s."id"
				LEFT JOIN "zt_project" p ON ps."project" = p."id"
				WHERE COALESCE(s."deleted", 0) = 0 AND p."type" IN ('sprint', 'stage')
				GROUP BY ps."project"`,
		},
		{
			name:      "ztv_projectbugs",
			dependsOn: []string{"zt_bug"},
			statement: `CREATE VIEW "ztv_projectbugs" AS
				SELECT "execution", COUNT(*) AS "bugs",
					SUM(CASE WHEN COALESCE("resolution", '') = '' THEN 0 ELSE 1 END) AS "resolutions",
					SUM(CASE WHEN "severity" <= 2 THEN 1 ELSE 0 END) AS "seriousBugs"
				FROM "zt_bug" WHERE COALESCE("deleted", 0) = 0 GROUP BY "execution"`,
		},
		{
			name:      "ztv_productbugs",
			dependsOn: []string{"zt_bug"},
			statement: `CREATE VIEW "ztv_productbugs" AS
				SELECT "product", COUNT(*) AS "bugs",
					SUM(CASE WHEN COALESCE("resolution", '') = '' THEN 0 ELSE 1 END) AS "resolutions",
					SUM(CASE WHEN "severity" <= 2 THEN 1 ELSE 0 END) AS "seriousBugs"
				FROM "zt_bug" WHERE COALESCE("deleted", 0) = 0 GROUP BY "product"`,
		},
		{
			name:      "ztv_productstories",
			dependsOn: []string{"zt_story"},
			statement: `CREATE VIEW "ztv_productstories" AS
				SELECT "product", COUNT(*) AS "stories", SUM(CASE WHEN "status" = 'closed' THEN 0 ELSE 1 END) AS "undone"
				FROM "zt_story" WHERE COALESCE("deleted", 0) = 0 GROUP BY "product"`,
		},
		{
			name:      "ztv_executionsummary",
			dependsOn: []string{"zt_task"},
			statement: `CREATE VIEW "ztv_executionsummary" AS
				SELECT "execution",
					SUM(CASE WHEN COALESCE("isParent", 0) = 0 THEN "estimate" ELSE 0 END) AS "estimate",
					SUM(CASE WHEN COALESCE("isParent", 0) = 0 THEN "consumed" ELSE 0 END) AS "consumed",
					SUM(CASE WHEN "status" NOT IN ('cancel', 'closed') AND COALESCE("isParent", 0) = 0 THEN "left" ELSE 0 END) AS "left",
					COUNT(*) AS "number",
					SUM(CASE WHEN "status" NOT IN ('done', 'closed') THEN 1 ELSE 0 END) AS "undone",
					SUM(
						CASE WHEN COALESCE("isParent", 0) = 0 THEN "consumed" ELSE 0 END
						+ CASE WHEN "status" NOT IN ('cancel', 'closed') AND COALESCE("isParent", 0) = 0 THEN "left" ELSE 0 END
					) AS "totalReal"
				FROM "zt_task" WHERE COALESCE("deleted", 0) = 0 GROUP BY "execution"`,
		},
		{
			name:      "ztv_projectsummary",
			dependsOn: []string{"zt_task"},
			statement: `CREATE VIEW "ztv_projectsummary" AS
				SELECT "project",
					SUM(CASE WHEN COALESCE("isParent", 0) = 0 THEN "estimate" ELSE 0 END) AS "estimate",
					SUM(CASE WHEN COALESCE("isParent", 0) = 0 THEN "consumed" ELSE 0 END) AS "consumed",
					SUM(CASE WHEN "status" NOT IN ('cancel', 'closed') AND COALESCE("isParent", 0) = 0 THEN "left" ELSE 0 END) AS "left",
					COUNT(*) AS "number",
					SUM(CASE WHEN "status" NOT IN ('done', 'closed') THEN 1 ELSE 0 END) AS "undone",
					SUM(
						CASE WHEN COALESCE("isParent", 0) = 0 THEN "consumed" ELSE 0 END
						+ CASE WHEN "status" NOT IN ('cancel', 'closed') AND COALESCE("isParent", 0) = 0 THEN "left" ELSE 0 END
					) AS "totalReal"
				FROM "zt_task" WHERE COALESCE("deleted", 0) = 0 GROUP BY "project"`,
		},
		{
			name:      "ztv_dayactions",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_dayactions" AS SELECT COUNT(*) AS "actions", CAST("date" AS DATE) AS "day" FROM "zt_action" GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_dayuserlogin",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_dayuserlogin" AS SELECT COUNT(*) AS "userlogin", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'user' AND "action" = 'login' GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_dayeffort",
			dependsOn: []string{"zt_effort"},
			statement: `CREATE VIEW "ztv_dayeffort" AS SELECT ROUND(SUM("consumed"), 1) AS "consumed", "date" FROM "zt_effort" GROUP BY "date"`,
		},
		{
			name:      "ztv_daystoryopen",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_daystoryopen" AS SELECT COUNT(*) AS "storyopen", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'story' AND "action" = 'opened' GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_daystoryclose",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_daystoryclose" AS SELECT COUNT(*) AS "storyclose", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'story' AND "action" = 'closed' GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_daytaskopen",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_daytaskopen" AS SELECT COUNT(*) AS "taskopen", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'task' AND "action" = 'opened' GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_daytaskfinish",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_daytaskfinish" AS SELECT COUNT(*) AS "taskfinish", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'task' AND "action" = 'finished' GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_daybugopen",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_daybugopen" AS SELECT COUNT(*) AS "bugopen", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'bug' AND "action" = 'opened' GROUP BY CAST("date" AS DATE)`,
		},
		{
			name:      "ztv_daybugresolve",
			dependsOn: []string{"zt_action"},
			statement: `CREATE VIEW "ztv_daybugresolve" AS SELECT COUNT(*) AS "bugresolve", CAST("date" AS DATE) AS "day" FROM "zt_action" WHERE "objectType" = 'bug' AND "action" = 'resolved' GROUP BY CAST("date" AS DATE)`,
		},
	}

	aliases := map[string]string{
		"ztv_build":       "zt_build",
		"ztv_product":     "zt_product",
		"ztv_productplan": "zt_productplan",
		"ztv_release":     "zt_release",
		"ztv_project":     "zt_project",
		"ztv_execution":   "zt_project",
		"ztv_task":        "zt_task",
		"ztv_bug":         "zt_bug",
		"ztv_bugbuild":    "zt_bug",
		"ztv_story":       "zt_story",
		"ztv_testcase":    "zt_case",
		"ztv_casestep":    "zt_casestep",
		"ztv_testtask":    "zt_testtask",
		"ztv_testrun":     "zt_testrun",
		"ztv_testresult":  "zt_testresult",
	}
	for name, table := range aliases {
		views = append(views, view{
			name:      name,
			dependsOn: []string{table},
			statement: fmt.Sprintf(`CREATE VIEW %s AS SELECT * FROM %s`, quoteSQLIdentifier(name), quoteSQLIdentifier(table)),
		})
	}

	for _, item := range views {
		if !manifestsExist(manifests, item.dependsOn...) {
			continue
		}
		if _, err := db.ExecContext(ctx, item.statement); err != nil {
			return fmt.Errorf("create OLAP view %s: %w", item.name, err)
		}
	}
	return nil
}

func manifestsExist(manifests map[string]Manifest, tables ...string) bool {
	for _, table := range tables {
		if _, exists := manifests[table]; !exists {
			return false
		}
	}
	return true
}
