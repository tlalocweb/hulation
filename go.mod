module github.com/tlalocweb/hulation

go 1.21

toolchain go1.22.0

replace gorm.io/driver/clickhouse => ../clickhouse

replace github.com/IzumaNetworks/conftagz => ../conftagz

require (
	github.com/ClickHouse/clickhouse-go/v2 v2.17.1
	github.com/IzumaNetworks/conftagz v0.0.6
	github.com/davecgh/go-spew v1.1.1
	github.com/gofiber/contrib/fiberzerolog v0.2.3
	github.com/gofiber/contrib/jwt v1.0.7
	github.com/gofiber/fiber/v2 v2.52.0
	github.com/gofiber/template/html/v2 v2.1.0
	github.com/golang-jwt/jwt/v5 v5.2.0
	github.com/google/uuid v1.6.0
	github.com/hashicorp/go-multierror v1.1.1
	github.com/joho/godotenv v1.5.1
	github.com/rs/zerolog v1.31.0
	github.com/stretchr/testify v1.8.4
	github.com/umputun/remark42/backend v1.12.1
	go.etcd.io/bbolt v1.3.8
	gopkg.in/yaml.v2 v2.4.0
	gorm.io/driver/clickhouse v0.6.0
	gorm.io/gorm v1.25.5
)

require (
	github.com/ClickHouse/ch-go v0.61.1 // indirect
	github.com/Depado/bfchroma/v2 v2.0.0 // indirect
	github.com/MicahParks/keyfunc/v2 v2.1.0 // indirect
	github.com/OneOfOne/xxhash v1.2.8 // indirect
	github.com/PuerkitoBio/goquery v1.8.1 // indirect
	github.com/agnivade/levenshtein v1.1.1 // indirect
	github.com/alecthomas/chroma/v2 v2.8.0 // indirect
	github.com/alphadose/haxmap v1.3.1 // indirect
	github.com/alphadose/zenq/v2 v2.8.3 // indirect
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/andybalholm/cascadia v1.3.2 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cbroglie/mustache v1.4.0 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/chzyer/readline v1.5.1 // indirect
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/gdamore/encoding v1.0.0 // indirect
	github.com/gdamore/tcell/v2 v2.6.1-0.20231203215052-2917c3801e73 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/go-logr/logr v1.4.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-pkgz/lgr v0.11.1 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/gofiber/contrib/opafiber/v2 v2.0.3 // indirect
	github.com/gofiber/fiber/v3 v3.0.0-20240213140423-ae8f09ac3b90 // indirect
	github.com/gofiber/template v1.8.2 // indirect
	github.com/gofiber/utils v1.1.0 // indirect
	github.com/gofiber/utils/v2 v2.0.0-beta.3 // indirect
	github.com/gorilla/css v1.0.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-version v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/klauspost/compress v1.17.6 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.15 // indirect
	github.com/matttproud/golang_protobuf_extensions/v2 v2.0.0 // indirect
	github.com/microcosm-cc/bluemonday v1.0.25 // indirect
	github.com/open-policy-agent/opa v0.61.0 // indirect
	github.com/paulmach/orb v0.11.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_golang v1.18.0 // indirect
	github.com/prometheus/client_model v0.5.0 // indirect
	github.com/prometheus/common v0.45.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	github.com/rcrowley/go-metrics v0.0.0-20200313005456-10cdbea86bc0 // indirect
	github.com/rivo/tview v0.0.0-20240204151237-861aa94d61c8 // indirect
	github.com/rivo/uniseg v0.4.7-0.20240127222946-601bbb3750c2 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/santhosh-tekuri/jsonschema v1.2.4 // indirect
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1 // indirect
	github.com/segmentio/asm v1.2.0 // indirect
	github.com/shopspring/decimal v1.3.1 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/tchap/go-patricia/v2 v2.3.1 // indirect
	github.com/tlalocweb/argon2id v0.0.0-20240207052003-0730dd790e46 // indirect
	github.com/tlalocweb/go-cache v0.0.1 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.52.0 // indirect
	github.com/valyala/tcplisten v1.0.0 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/yashtewari/glob-intersection v0.2.0 // indirect
	go.opentelemetry.io/otel v1.22.0 // indirect
	go.opentelemetry.io/otel/metric v1.22.0 // indirect
	go.opentelemetry.io/otel/sdk v1.21.0 // indirect
	go.opentelemetry.io/otel/trace v1.22.0 // indirect
	golang.org/x/crypto v0.19.0 // indirect
	golang.org/x/exp v0.0.0-20230510235704-dd950f8aeaea // indirect
	golang.org/x/net v0.21.0 // indirect
	golang.org/x/sys v0.17.0 // indirect
	golang.org/x/term v0.17.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/protobuf v1.31.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	sigs.k8s.io/yaml v1.4.0 // indirect
)
