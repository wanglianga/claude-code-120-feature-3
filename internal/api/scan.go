package api

import (
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// scanMaps 把任意查询结果扫描为 []map（列名取自结果集元数据）。
func scanMaps(rows pgx.Rows) []map[string]any {
	out := []map[string]any{}
	fd := rows.FieldDescriptions()
	names := make([]string, len(fd))
	for i, f := range fd {
		names[i] = f.Name
	}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			continue
		}
		m := map[string]any{}
		for i, v := range vals {
			m[names[i]] = normalize(v)
		}
		out = append(out, m)
	}
	return out
}

func normalize(v any) any {
	switch t := v.(type) {
	case time.Time:
		return t.Format("2006-01-02 15:04:05Z07:00")
	case []byte:
		return string(t)
	case pgtype.Numeric:
		return numericToFloat(t)
	case fmt.Stringer:
		return t.String()
	default:
		return v
	}
}

// numericToFloat 把 pgtype.Numeric（Int×10^Exp）转为 float64；无效/NaN 返回 nil。
func numericToFloat(n pgtype.Numeric) any {
	if !n.Valid || n.NaN || n.Int == nil {
		return nil
	}
	f := new(big.Float).SetInt(n.Int)
	ten := big.NewInt(10)
	if n.Exp > 0 {
		f.Mul(f, new(big.Float).SetInt(new(big.Int).Exp(ten, big.NewInt(int64(n.Exp)), nil)))
	} else if n.Exp < 0 {
		f.Quo(f, new(big.Float).SetInt(new(big.Int).Exp(ten, big.NewInt(int64(-n.Exp)), nil)))
	}
	out, _ := f.Float64()
	return out
}
