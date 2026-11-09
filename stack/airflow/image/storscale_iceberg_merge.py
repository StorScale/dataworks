"""dlt's merge into an Iceberg table, made to take a batch without every column.

dlt adds a column to its schema only once a value comes for it, so in a load
where a column first has a value partway through (a field a source had kept
empty), the load's files written before it lack the column. dlt's merge
(dlt.common.libs.pyiceberg.merge_iceberg_table, 1.30.0) evolves the table to
the file's columns and then upserts with pyiceberg, which casts the file to the
table's schema by name and position, and refuses one that lacks a column: "Target
schema's field names are not matching the table's field names". The load then
fails on every run, retrying the same package, and nothing newer is loaded.

Which way it fails depends on the order dlt loads the package's files in: the
file without the column after the one with it (the table's rows have it, the
file not), or the other way (the file has it, the table's rows, read at the last
snapshot, not). apply() handles both:

  - dlt's merge: once the table (read afresh, since dlt loads a package's files
    at once) has the file's columns, the file gets the table's, in its order, a
    column it lacks as nulls (what dlt would have written for those rows);
  - pyiceberg's comparison of the file with the table's matching rows: each
    gets the other's missing columns as nulls.
"""
import pyarrow as pa
from dlt.common.libs import pyiceberg as dlt_pyiceberg


def conform(table, data: pa.Table) -> pa.Table:
    """data with the Iceberg table's columns, in its order: those it lacks as nulls. The table is read
    afresh first: dlt loads a package's files at once, and another of them may have just added a column."""
    table.refresh()
    with table.update_schema() as update:   # as dlt's merge does first: the file's new columns in the table
        update.union_by_name(dlt_pyiceberg.ensure_iceberg_compatible_arrow_schema(data.schema))
    wanted = table.schema().as_arrow()
    if wanted.names == data.column_names:
        return data
    arrays, fields = [], []
    for f in wanted:
        if f.name in data.column_names:
            arrays.append(data.column(f.name))
            fields.append(data.schema.field(f.name))
        else:
            arrays.append(pa.nulls(data.num_rows, f.type))
            fields.append(pa.field(f.name, f.type, nullable=True))
    return pa.Table.from_arrays(arrays, schema=pa.schema(fields))


def aligned(source: pa.Table, target: pa.Table):
    """pyiceberg's upsert compares the file (source) with the table's matching rows (target), which it
    reads at the table's last snapshot: before a column the file just added, or with one the file
    lacks. Each gets the other's missing columns as nulls, the target in the source's order."""
    for name in target.column_names:
        if name not in source.column_names:
            source = source.append_column(target.schema.field(name).with_nullable(True), pa.nulls(source.num_rows, target.schema.field(name).type))
    for name in source.column_names:
        if name not in target.column_names:
            target = target.append_column(source.schema.field(name).with_nullable(True), pa.nulls(target.num_rows, source.schema.field(name).type))
    return source, target.select(source.column_names)


def apply():
    """Have dlt's filesystem destination merge through conform (it imports the function as it loads a job)."""
    merge = dlt_pyiceberg.merge_iceberg_table
    if getattr(merge, "storscale_conformed", False):
        return

    def conformed(table, data, schema, load_table_name):
        return merge(table, conform(table, data), schema, load_table_name)

    conformed.storscale_conformed = True
    dlt_pyiceberg.merge_iceberg_table = conformed

    from pyiceberg.table import upsert_util
    rows_to_update = upsert_util.get_rows_to_update

    def rows_to_update_aligned(source_table, target_table, join_cols):
        return rows_to_update(*aligned(source_table, target_table), join_cols)

    upsert_util.get_rows_to_update = rows_to_update_aligned
