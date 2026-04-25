module github.com/tlalocweb/hulation

go 1.26

replace gorm.io/driver/clickhouse => ../clickhouse

require (
	github.com/ClickHouse/clickhouse-go/v2 v2.43.0
	github.com/alexedwards/argon2id v1.0.0
	github.com/alphadose/haxmap v1.4.1
	github.com/bytemare/ecc v0.9.0
	github.com/bytemare/opaque v0.18.0
	github.com/cbroglie/mustache v1.4.0
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/chzyer/readline v1.5.1
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc
	github.com/docker/docker v28.5.2+incompatible
	github.com/docker/go-connections v0.6.0
	github.com/emersion/go-webdav v0.7.0
	github.com/fsnotify/fsnotify v1.9.0
	github.com/go-logr/logr v1.4.3
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.7
	github.com/hashicorp/go-immutable-radix/v2 v2.1.0
	github.com/hashicorp/go-multierror v1.1.1
	github.com/hashicorp/go-version v1.8.0
	github.com/hashicorp/raft v1.7.3
	github.com/hashicorp/raft-boltdb v0.0.0-20251103221153-05f9dd7a5148
	github.com/open-policy-agent/opa v1.14.1
	github.com/pires/go-proxyproto v0.12.0
	github.com/pquerna/otp v1.5.0
	github.com/risor-io/risor v1.8.1
	github.com/rivo/tview v0.42.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/rs/zerolog v1.34.0
	github.com/santhosh-tekuri/jsonschema v1.2.4
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
	github.com/stretchr/testify v1.11.1
	github.com/tlalocweb/argon2id v0.0.0-20240207052003-0730dd790e46
	github.com/tlalocweb/go-cache v0.0.1
	github.com/ua-parser/uap-go v0.0.0-20251207011819-db9adb27a0b8
	github.com/umputun/remark42/backend v1.14.0
	go.etcd.io/bbolt v1.4.3
	go.izuma.io/conftagz v0.0.19
	golang.org/x/crypto v0.49.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/text v0.35.0
	golang.org/x/time v0.15.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260128011058-8636f8732409
	google.golang.org/grpc v1.79.1
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/clickhouse v0.7.0
	gorm.io/gorm v1.31.1
)

require (
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	filippo.io/edwards25519 v1.2.0 // indirect
	filippo.io/nistec v0.0.4 // indirect
	github.com/ClickHouse/ch-go v0.71.0 // indirect
	github.com/Depado/bfchroma/v2 v2.0.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/PuerkitoBio/goquery v1.10.3 // indirect
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/alecthomas/chroma/v2 v2.20.0 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/armon/go-metrics v0.4.1 // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/boltdb/bolt v1.3.1 // indirect
	github.com/boombuler/barcode v1.0.1 // indirect
	github.com/bytemare/hash v0.6.2 // indirect
	github.com/bytemare/hash2curve v0.5.4 // indirect
	github.com/bytemare/ksf v0.5.0 // indirect
	github.com/bytemare/secp256k1 v0.3.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.2.0 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/dlclark/regexp2 v1.11.5 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/fatih/color v1.15.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gdamore/encoding v1.0.1 // indirect
	github.com/gdamore/tcell/v2 v2.9.0 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-pkgz/lgr v0.12.1 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/gtank/ristretto255 v0.2.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-hclog v1.6.2 // indirect
	github.com/hashicorp/go-immutable-radix v1.0.0 // indirect
	github.com/hashicorp/go-metrics v0.5.4 // indirect
	github.com/hashicorp/go-msgpack v0.5.5 // indirect
	github.com/hashicorp/go-msgpack/v2 v2.1.2 // indirect
	github.com/hashicorp/golang-lru v1.0.2 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/klauspost/compress v1.18.3 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.0.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc/v3 v3.0.2 // indirect
	github.com/lestrrat-go/jwx/v3 v3.0.13 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/microcosm-cc/bluemonday v1.0.27 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/sys/atomicwriter v0.1.0 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/paulmach/orb v0.12.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.25 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/common v0.67.1 // indirect
	github.com/rcrowley/go-metrics v0.0.0-20250401214520-65e299d6c5c9 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tchap/go-patricia/v2 v2.3.3 // indirect
	github.com/valyala/fastjson v1.6.7 // indirect
	github.com/vektah/gqlparser/v2 v2.5.32 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/yashtewari/glob-intersection v0.2.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.65.0 // indirect
	go.opentelemetry.io/otel v1.40.0 // indirect
	go.opentelemetry.io/otel/metric v1.40.0 // indirect
	go.opentelemetry.io/otel/trace v1.40.0 // indirect
	go.yaml.in/yaml/v2 v2.4.3 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/exp v0.0.0-20251017212417-90e834f514db // indirect
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/term v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260128011058-8636f8732409 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)
