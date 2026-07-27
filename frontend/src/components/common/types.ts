/**
 * Common component types
 */

export interface Column {
  key: string
  label: string
  sortable?: boolean
  /** Sort this column in the already-loaded rows even when the table otherwise uses server-side sorting. */
  clientSideSort?: boolean
  /** Derive the comparable value when a column displays data from a nested or computed field. */
  sortValue?: (row: any) => unknown
  /** Keep missing values below present values for both ascending and descending sorts. */
  sortNullsLast?: boolean
  class?: string
  formatter?: (value: any, row: any) => string
}
