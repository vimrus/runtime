// Package olap defines shared OLAP dataset and SQL compatibility contracts.
package olap

const (
	// SQLCompatibilityVersion identifies the bundled MySQL compatibility layer.
	SQLCompatibilityVersion = 2
)

// DuckDBCompatibilitySQL returns the versioned macro bootstrap required before
// executing built-in OLAP SQL on DuckDB. The script must remain internal and may
// not install or load external extensions.
func DuckDBCompatibilitySQL() string {
	return `
CREATE MACRO date_format(d, fmt)
AS strftime(d, fmt);

CREATE MACRO datediff(end_date, start_date)
AS date_diff('day', CAST(start_date AS DATE), CAST(end_date AS DATE));

CREATE MACRO date_sub(d, i)
AS d - i;

CREATE MACRO find_in_set(target, list)
AS CASE
     WHEN list IS NULL OR list = '' THEN 0
     ELSE COALESCE(
       list_position(string_split(list, ','), CAST(target AS VARCHAR)),
       0
     )
   END;

CREATE MACRO truncate(value, scale)
AS trunc(CAST(value AS DOUBLE) * pow(10, scale)) / pow(10, scale);
`
}
