// Package providers registers the official Tushare API and the user-provided
// HTTP contract. Neither adapter is enabled until explicitly configured.
package providers

import "github.com/quant4dad/internal/repository/datasource"

func init() {
	datasource.Register("http", newHTTP)
	datasource.Register("tushare", newTushare)
}
