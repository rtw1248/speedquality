#!/usr/bin/env bash

set -Eeuo pipefail

readonly SPEEDQUALITY_VERSION="1.0.0"
readonly FALLBACK_PROBE_VERSION="v1.0.0"
readonly DEFAULT_PROBE_VERSION="__SPEEDQUALITY_PROBE_VERSION__"
readonly PROBE_VERSION_PLACEHOLDER="__SPEEDQUALITY_""PROBE_VERSION__"
readonly DEFAULT_NODEQUALITY_API="https://api.nodequality.com/api/v1"
readonly DEFAULT_NODEQUALITY_ORIGIN="https://nodequality.com"
readonly DEFAULT_GEO_API="https://ipwho.is"
readonly DEFAULT_REPORT_BASE="__SPEEDQUALITY_REPORT_BASE__"
readonly REPORT_BASE_PLACEHOLDER="__SPEEDQUALITY_""REPORT_BASE__"
readonly MAX_TIME_GAP_MINUTES=60
readonly NODEQUALITY_USER_AGENT="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36"
readonly SPEED_DURATION_SECONDS=5
readonly MAX_SELECTED_PROVINCES=5
readonly TEMP_MARKER_NAME=".speedquality-owned"

PROBE_VERSION="${SPEEDQUALITY_PROBE_VERSION:-$DEFAULT_PROBE_VERSION}"
if [[ "$PROBE_VERSION" == "$PROBE_VERSION_PLACEHOLDER" ]]; then
  PROBE_VERSION="$FALLBACK_PROBE_VERSION"
fi
NODEQUALITY_API="${SPEEDQUALITY_NODEQUALITY_API:-$DEFAULT_NODEQUALITY_API}"
NODEQUALITY_ORIGIN="${SPEEDQUALITY_NODEQUALITY_ORIGIN:-$DEFAULT_NODEQUALITY_ORIGIN}"
GEO_API="${SPEEDQUALITY_GEO_API:-$DEFAULT_GEO_API}"
CACHE_DIR="${SPEEDQUALITY_CACHE_DIR:-${XDG_CACHE_HOME:-${HOME:-/tmp}/.cache}/speedquality}"
REPORT_BASE="${SPEEDQUALITY_SERVICE_BASE:-${SPEEDQUALITY_REPORT_BASE:-$DEFAULT_REPORT_BASE}}"
PROBE_BASE="${SPEEDQUALITY_PROBE_BASE:-}"

NODEQUALITY_URL=""
NODE_ROUTE_KEY=""
NODE_ROUTE_REGION=""
NODE_ROUTE_CARRIER=""
NODE_ROUTE_ACCESS_MODE=""
NODE_ROUTE_MAX_MBPS=0
NODE_ROUTE_HAS_V4=0
NODE_ROUTE_HAS_V6=0
SPEED_DATA_FILE=""
DISPLAY_URL=""
RESULT_PAGE_URL=""
BIND_STATUS="standalone"
TEMP_DIR=""
SPEED_LOG=""
SPEED_DIAGNOSTIC_LOG="${SPEEDQUALITY_DIAGNOSTIC_LOG:-}"
SPEED_TEST_EPOCH=0
NODEQUALITY_TIME_GAP_SECONDS=""
NODEQUALITY_REPORT_EPOCH=""
NODEQUALITY_TIME_SOURCE=""
NODEQUALITY_SNAPSHOT_FILE=""
NODEQUALITY_IDENTITY_REASON=""
NODEQUALITY_BINDING_ENABLED=1
REGION_INPUT=""
SELECTED_POINTS=""
SELECTED_REGION_CODES=""
SPEED_MODE="s"
SPEED_TARGET_MBPS=200
IP_FAMILY_SELECTION="auto"
IP_MODE="v4"
SSH_CLIENT_IP=""
AUTO_REGION_CODE=""
AUTO_REGION_NAME=""
ORIGINAL_ARG_COUNT=0
TRAFFIC_RX_BYTES=""
TRAFFIC_TX_BYTES=""
TRAFFIC_TOTAL_BYTES=""
TRAFFIC_INTERFACES=""

SESSION_TOKEN=""
SESSION_TOKEN_V4=""
SESSION_TOKEN_V6=""
PROBE_BINARY=""
RUN_FAMILIES=""
PRIMARY_FAMILY=""
PRIMARY_CURL_FAMILY=""

if [[ -t 2 && "${TERM:-dumb}" != "dumb" ]]; then
  C_CYAN=$'\033[36m'
  C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'
  C_RED=$'\033[31m'
  C_RESET=$'\033[0m'
else
  C_CYAN=""
  C_GREEN=""
  C_YELLOW=""
  C_RED=""
  C_RESET=""
fi

info() {
  printf '%s[i]%s %s\n' "$C_CYAN" "$C_RESET" "$*" >&2
}

success() {
  printf '%s[+]%s %s\n' "$C_GREEN" "$C_RESET" "$*" >&2
}

warn() {
  printf '%s[!]%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2
}

alert() {
  printf '%s[!]%s %s\n' "$C_RED" "$C_RESET" "$*" >&2
}

die() {
  printf '%s[X]%s %s\n' "$C_RED" "$C_RESET" "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
SpeedQuality - 服务器分地区测速与 NodeQuality 结果绑定工具

用法:
  bash run.sh [SpeedQuality 选项]

SpeedQuality 选项:
  -p, --province VALUE    省份：auto、bsg、hb、hb,bj、湖北，北京等，最多 5 个
  -s, --speed VALUE       测速档位及最高速率：100、200、400 Mbps，默认 200
  -v4, --ipv4             仅测试 IPv4；默认自动测试可用的 IPv4 和 IPv6
  -v6, --ipv6             仅测试 IPv6；不能与 -v4 同时使用
      --nq URL            绑定已有 NodeQuality 报告 URL 或报告 token
      --node ROUTE_KEY    精确使用自己的 SQ 节点；不传 -p 时采用节点登记省份
  -l, --list-provinces    显示支持的省份代码
  -h, --help              显示帮助
      --version           显示脚本和探针版本

地区代码:
  bsg SSH 来源省份 + 北京、上海、广东；示例：hb 湖北、bj 北京、gd 广东
  多个省份可使用中英文逗号或顿号分隔；单次最多 5 个，完整列表使用 --list-provinces 查看。

示例:
  bash run.sh
  bash run.sh -p hb -s 200
  bash run.sh -p '湖北，北京' -s 100 -v4
  bash run.sh -p hb -s 100 -v6
  bash run.sh -p hb --nq https://nodequality.com/r/REPORT_TOKEN
  bash run.sh --node sqn_YOUR_ROUTE_KEY -s 200
EOF
}

cleanup() {
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" &&
        "$(basename -- "$TEMP_DIR")" == speedquality.* &&
        -f "$TEMP_DIR/$TEMP_MARKER_NAME" ]]; then
    rm -rf -- "$TEMP_DIR"
  fi
}
trap cleanup EXIT

require_value() {
  local option="$1"
  local count="$2"
  ((count >= 2)) || die "$option 缺少参数"
}

region_name() {
  case "$1" in
    bj) printf '北京' ;; tj) printf '天津' ;; he) printf '河北' ;;
    sx) printf '山西' ;; nm) printf '内蒙古' ;; ln) printf '辽宁' ;;
    jl) printf '吉林' ;; hl) printf '黑龙江' ;; sh) printf '上海' ;;
    js) printf '江苏' ;; zj) printf '浙江' ;; ah) printf '安徽' ;;
    fj) printf '福建' ;; jx) printf '江西' ;; sd) printf '山东' ;;
    ha) printf '河南' ;; hb) printf '湖北' ;; hn) printf '湖南' ;;
    gd) printf '广东' ;; gx) printf '广西' ;; hi) printf '海南' ;;
    cq) printf '重庆' ;; sc) printf '四川' ;; gz) printf '贵州' ;;
    yn) printf '云南' ;; xz) printf '西藏' ;; sn) printf '陕西' ;;
    gs) printf '甘肃' ;; qh) printf '青海' ;; nx) printf '宁夏' ;;
    xj) printf '新疆' ;; tw) printf '台湾' ;; hk) printf '香港' ;;
    mo) printf '澳门' ;;
    *) return 1 ;;
  esac
}

region_code_from_token() {
  local token="$1"
  token="${token//[[:space:]]/}"
  token="${token,,}"
  case "$token" in
    bj|北京|北京市) printf 'bj' ;; tj|天津|天津市) printf 'tj' ;;
    he|河北|河北省) printf 'he' ;; sx|山西|山西省) printf 'sx' ;;
    nm|内蒙古|内蒙古自治区) printf 'nm' ;; ln|辽宁|辽宁省) printf 'ln' ;;
    jl|吉林|吉林省) printf 'jl' ;; hl|黑龙江|黑龙江省) printf 'hl' ;;
    sh|上海|上海市) printf 'sh' ;; js|江苏|江苏省) printf 'js' ;;
    zj|浙江|浙江省) printf 'zj' ;; ah|安徽|安徽省) printf 'ah' ;;
    fj|福建|福建省) printf 'fj' ;; jx|江西|江西省) printf 'jx' ;;
    sd|山东|山东省) printf 'sd' ;; ha|河南|河南省) printf 'ha' ;;
    hb|湖北|湖北省) printf 'hb' ;; hn|湖南|湖南省) printf 'hn' ;;
    gd|广东|广东省) printf 'gd' ;; gx|广西|广西壮族自治区) printf 'gx' ;;
    hi|海南|海南省) printf 'hi' ;; cq|重庆|重庆市) printf 'cq' ;;
    sc|四川|四川省) printf 'sc' ;; gz|贵州|贵州省) printf 'gz' ;;
    yn|云南|云南省) printf 'yn' ;; xz|西藏|西藏自治区) printf 'xz' ;;
    sn|陕西|陕西省) printf 'sn' ;; gs|甘肃|甘肃省) printf 'gs' ;;
    qh|青海|青海省) printf 'qh' ;; nx|宁夏|宁夏回族自治区) printf 'nx' ;;
    xj|新疆|新疆维吾尔自治区) printf 'xj' ;; tw|台湾|台湾省) printf 'tw' ;;
    hk|香港|香港特别行政区) printf 'hk' ;; mo|澳门|澳门特别行政区) printf 'mo' ;;
    *) return 1 ;;
  esac
}

list_provinces() {
  cat <<'EOF'
地区代码（山西 sx，陕西 sn）：
  bj 北京    tj 天津    he 河北    sx 山西    nm 内蒙古
  ln 辽宁    jl 吉林    hl 黑龙江  sh 上海    js 江苏
  zj 浙江    ah 安徽    fj 福建    jx 江西    sd 山东
  ha 河南    hb 湖北    hn 湖南    gd 广东    gx 广西
  hi 海南    cq 重庆    sc 四川    gz 贵州    yn 云南
  xz 西藏    sn 陕西    gs 甘肃    qh 青海    nx 宁夏
  xj 新疆    tw 台湾    hk 香港    mo 澳门
  bsg SSH 来源省份 + 北京、上海、广东（自动去重）

  易混代码均保持唯一：河北 he / 湖北 hb，河南 ha / 湖南 hn，山西 sx / 陕西 sn。
  单次最多选择 5 个省级地区；全国 all 测试已关闭。
EOF
}

city_province_hint() {
  local city="${1%市}"
  case "$city" in
    石家庄|唐山|秦皇岛|邯郸|保定|张家口|承德|沧州|廊坊|衡水) printf 'he|河北' ;;
    太原|大同|阳泉|长治|晋城|朔州|晋中|运城|忻州|临汾|吕梁) printf 'sx|山西' ;;
    沈阳|大连|鞍山|抚顺|本溪|丹东|锦州|营口|阜新|辽阳|盘锦|铁岭|朝阳|葫芦岛) printf 'ln|辽宁' ;;
    长春|吉林|四平|辽源|通化|白山|松原|白城) printf 'jl|吉林' ;;
    哈尔滨|齐齐哈尔|鸡西|鹤岗|双鸭山|大庆|伊春|佳木斯|七台河|牡丹江|黑河|绥化) printf 'hl|黑龙江' ;;
    南京|无锡|徐州|常州|苏州|南通|连云港|淮安|盐城|扬州|镇江|泰州|宿迁) printf 'js|江苏' ;;
    杭州|宁波|温州|嘉兴|湖州|绍兴|金华|衢州|舟山|台州|丽水) printf 'zj|浙江' ;;
    合肥|芜湖|蚌埠|淮南|马鞍山|淮北|铜陵|安庆|黄山|滁州|阜阳|宿州|六安|亳州|池州|宣城) printf 'ah|安徽' ;;
    福州|厦门|莆田|三明|泉州|漳州|南平|龙岩|宁德) printf 'fj|福建' ;;
    南昌|景德镇|萍乡|九江|新余|鹰潭|赣州|吉安|宜春|抚州|上饶) printf 'jx|江西' ;;
    济南|青岛|淄博|枣庄|东营|烟台|潍坊|济宁|泰安|威海|日照|临沂|德州|聊城|滨州|菏泽) printf 'sd|山东' ;;
    郑州|开封|洛阳|平顶山|安阳|鹤壁|新乡|焦作|濮阳|许昌|漯河|三门峡|南阳|商丘|信阳|周口|驻马店) printf 'ha|河南' ;;
    武汉|黄石|十堰|宜昌|襄阳|鄂州|荆门|孝感|荆州|黄冈|咸宁|随州) printf 'hb|湖北' ;;
    长沙|株洲|湘潭|衡阳|邵阳|岳阳|常德|张家界|益阳|郴州|永州|怀化|娄底) printf 'hn|湖南' ;;
    广州|韶关|深圳|珠海|汕头|佛山|江门|湛江|茂名|肇庆|惠州|梅州|汕尾|河源|阳江|清远|东莞|中山|潮州|揭阳|云浮) printf 'gd|广东' ;;
    南宁|柳州|桂林|梧州|北海|防城港|钦州|贵港|玉林|百色|贺州|河池|来宾|崇左) printf 'gx|广西' ;;
    海口|三亚|三沙|儋州) printf 'hi|海南' ;;
    成都|自贡|攀枝花|泸州|德阳|绵阳|广元|遂宁|内江|乐山|南充|眉山|宜宾|广安|达州|雅安|巴中|资阳) printf 'sc|四川' ;;
    贵阳|六盘水|遵义|安顺|毕节|铜仁) printf 'gz|贵州' ;;
    昆明|曲靖|玉溪|保山|昭通|丽江|普洱|临沧) printf 'yn|云南' ;;
    拉萨|日喀则|昌都|林芝|山南|那曲) printf 'xz|西藏' ;;
    西安|铜川|宝鸡|咸阳|渭南|延安|汉中|榆林|安康|商洛) printf 'sn|陕西' ;;
    兰州|嘉峪关|金昌|白银|天水|武威|张掖|平凉|酒泉|庆阳|定西|陇南) printf 'gs|甘肃' ;;
    西宁|海东) printf 'qh|青海' ;;
    银川|石嘴山|吴忠|固原|中卫) printf 'nx|宁夏' ;;
    乌鲁木齐|克拉玛依|吐鲁番|哈密) printf 'xj|新疆' ;;
    呼和浩特|包头|乌海|赤峰|通辽|鄂尔多斯|呼伦贝尔|巴彦淖尔|乌兰察布) printf 'nm|内蒙古' ;;
    *) return 1 ;;
  esac
}

normalize_regions() {
  local text="$1"
  local token code name hint hint_code hint_name
  local -a tokens names codes
  local -A seen=()

  text="${text//，/,}"
  text="${text//、/,}"
  text="${text//；/,}"
  text="${text//;/,}"
  text="${text//$'\t'/,}"
  text="${text// /,}"
  IFS=',' read -r -a tokens <<< "$text"

  if ((${#tokens[@]} == 1)) && [[ "${tokens[0],,}" =~ ^(bsg|北上广)$ ]]; then
    [[ -n "$AUTO_REGION_CODE" ]] || die "bsg 需要先识别 SSH 来源省份；请改用 -p 明确列出地区，例如 -p hb,bj,sh,gd"
    tokens=("$AUTO_REGION_CODE" bj sh gd)
  else
    for token in "${tokens[@]}"; do
      case "${token,,}" in
        all) die "全国 all 测试已关闭；请使用 -p bsg 或明确选择最多 5 个省份" ;;
        bsg|北上广) die "bsg 需要单独使用；它会自动加入 SSH 来源省份、北京、上海和广东" ;;
      esac
    done
  fi

  for token in "${tokens[@]}"; do
    [[ -n "$token" ]] || continue
    if ! code=$(region_code_from_token "$token"); then
      if hint=$(city_province_hint "$token" 2>/dev/null); then
        IFS='|' read -r hint_code hint_name <<< "$hint"
        die "“$token”是城市名称，SpeedQuality 按省级地区测速；请使用 -p $hint_code 或 -p $hint_name"
      fi
      die "不支持的省级地区: $token（使用 --list-provinces 查看代码）"
    fi
    [[ -z "${seen[$code]:-}" ]] || continue
    seen[$code]=1
    name=$(region_name "$code")
    codes+=("$code")
    names+=("$name")
  done
  ((${#names[@]} > 0)) || die "没有选择测速地区"
  ((${#names[@]} <= MAX_SELECTED_PROVINCES)) || \
    die "单次最多测试 ${MAX_SELECTED_PROVINCES} 个省份；当前选择了 ${#names[@]} 个"

  SELECTED_POINTS=$(IFS=','; printf '%s' "${names[*]}")
  SELECTED_REGION_CODES=$(IFS=','; printf '%s' "${codes[*]}")
}

format_bytes() {
  LC_ALL=C awk -v bytes="${1:-0}" 'BEGIN {
    if (bytes >= 1000000000000) printf "%.2f TB", bytes / 1000000000000;
    else if (bytes >= 1000000000) printf "%.2f GB", bytes / 1000000000;
    else if (bytes >= 1000000) printf "%.2f MB", bytes / 1000000;
    else if (bytes >= 1000) printf "%.2f KB", bytes / 1000;
    else printf "%.0f B", bytes;
  }'
}

show_traffic_preflight() {
  local region_count family_count phase_seconds operator_points point_description
  local bytes_per_second estimated_bytes answer

  region_count=$(LC_ALL=C awk -F',' '{print NF}' <<< "$SELECTED_POINTS")
  family_count=$(wc -w <<< "$RUN_FAMILIES")
  phase_seconds=$((SPEED_DURATION_SECONDS + 2))
  if [[ -n "$NODE_ROUTE_KEY" ]]; then
    operator_points=1
    point_description="1 个指定节点"
  else
    operator_points=$((region_count * 3))
    point_description="${operator_points} 个运营商测速点"
  fi
  bytes_per_second=$((SPEED_TARGET_MBPS * 1000000 / 8))
  estimated_bytes=$((bytes_per_second * 2 * phase_seconds * operator_points * family_count))

  info "流量估算: ${region_count} 个省级地区 / ${point_description}，${family_count} 种 IP 类型，每方向 ${SPEED_DURATION_SECONDS} 秒 + 约 2 秒预热"
  warn "按上传、下载各 ${SPEED_TARGET_MBPS} Mbps 上限估算约 $(format_bytes "$estimated_bytes")"
  info "这是最大参考值；线路未达到目标速率时，实际消耗会更低"

  if ((estimated_bytes >= 10000000000)); then
    alert "当前方案可能消耗大量流量。可通过减少省份、降低 -s/--speed 或用 -v4/-v6 只测一种 IP 类型来降低消耗"
    [[ -t 0 ]] || die "高流量测速必须在交互终端中确认"
    printf '  继续执行高流量测速？[y/N]: ' >&2
    IFS= read -r answer || answer=""
    case "${answer,,}" in
      y|yes) ;;
      *) die "已取消测速" ;;
    esac
  fi
}

valid_ip_literal() {
  local ip="$1"
  local octet
  local -a octets
  ip="${ip#[}"
  ip="${ip%]}"
  ip="${ip%%%*}"
  if [[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
    IFS='.' read -r -a octets <<< "$ip"
    for octet in "${octets[@]}"; do
      ((10#$octet <= 255)) || return 1
    done
    return 0
  fi
  [[ "$ip" == *:* && "$ip" =~ ^[0-9A-Fa-f:.]+$ ]]
}

private_ip_literal() {
  local ip="$1"
  case "$ip" in
    10.*|127.*|169.254.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*|::1|fe80:*|fc*:*|fd*:*) return 0 ;;
    *) return 1 ;;
  esac
}

extract_ssh_client_ip() {
  local candidate="${SPEEDQUALITY_SSH_CLIENT_IP:-}"
  local who_ip=""
  if [[ -z "$candidate" && -n "${SSH_CONNECTION:-}" ]]; then
    candidate="${SSH_CONNECTION%% *}"
  fi
  if [[ -z "$candidate" && -n "${SSH_CLIENT:-}" ]]; then
    candidate="${SSH_CLIENT%% *}"
  fi
  if [[ -z "$candidate" ]] && command -v who >/dev/null 2>&1; then
    who_ip=$(who -m --ips 2>/dev/null | sed -n 's/.*(\([^()]\+\)).*/\1/p' | head -n 1)
    candidate="$who_ip"
  fi
  candidate="${candidate#[}"
  candidate="${candidate%]}"
  candidate="${candidate%%%*}"
  valid_ip_literal "$candidate" || return 1
  printf '%s\n' "$candidate"
}

json_string_field() {
  local file="$1"
  local key="$2"
  sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$file" | head -n 1
}

detect_auto_region() {
  local response_file="$TEMP_DIR/ssh-geo.json"
  local region_code country_code mapped

  SSH_CLIENT_IP=$(extract_ssh_client_ip || true)
  if [[ -z "$SSH_CLIENT_IP" ]]; then
    warn "没有检测到 SSH 客户端 IP；请手动选择地区"
    return 1
  fi
  if private_ip_literal "$SSH_CLIENT_IP"; then
    warn "SSH 来源 $SSH_CLIENT_IP 是内网地址，无法自动定位；请手动选择地区"
    return 1
  fi

  if [[ -n "${SPEEDQUALITY_GEO_RESPONSE_FILE:-}" ]]; then
    response_file="$SPEEDQUALITY_GEO_RESPONSE_FILE"
    [[ -r "$response_file" ]] || die "地区响应文件不可读: $response_file"
  else
    command -v curl >/dev/null 2>&1 || die "缺少 curl"
    validate_https_url "$GEO_API" || die "地区检测 API 地址不合法"
    if ! curl --proto '=https' --tlsv1.2 -fsSL --retry 2 --connect-timeout 5 --max-time 15 \
      "${GEO_API%/}/$SSH_CLIENT_IP" -o "$response_file"; then
      warn "无法查询 SSH 来源 $SSH_CLIENT_IP 的地区；请手动选择"
      return 1
    fi
  fi

  region_code=$(json_string_field "$response_file" region_code)
  country_code=$(json_string_field "$response_file" country_code)
  region_code="${region_code,,}"
  country_code="${country_code,,}"
  case "$country_code" in
    cn) ;;
    hk|mo|tw) region_code="$country_code" ;;
    *)
      warn "SSH 来源 $SSH_CLIENT_IP 不在支持的中国省级地区内；请手动选择"
      return 1
      ;;
  esac
  mapped=$(region_code_from_token "$region_code" || true)
  if [[ -z "$mapped" ]]; then
    warn "SSH 来源 $SSH_CLIENT_IP 不在支持的中国省级地区内；请手动选择"
    return 1
  fi
  AUTO_REGION_CODE="$mapped"
  AUTO_REGION_NAME=$(region_name "$mapped")
  return 0
}

interactive_region_selection() {
  local answer default_region
  printf '\n%sSSH 来源地区%s\n' "$C_CYAN" "$C_RESET"
  if [[ -n "$AUTO_REGION_CODE" ]]; then
    printf '  检测到 %s -> %s (%s)\n' "$SSH_CLIENT_IP" "$AUTO_REGION_NAME" "$AUTO_REGION_CODE"
    printf '  如果经过 SSH 跳板机，请手动改成你的实际地区。\n'
  elif [[ -n "$SSH_CLIENT_IP" ]]; then
    printf '  已检测到来源 %s，但无法自动映射地区。\n' "$SSH_CLIENT_IP"
  fi
  default_region="${REGION_INPUT:-$AUTO_REGION_CODE}"
  printf '  测速地区 [默认 %s，可填 bsg 或 hb,bj，最多 5 个]: ' "${default_region:-无}"
  IFS= read -r answer || answer=""
  REGION_INPUT="${answer:-$default_region}"
  [[ -n "$REGION_INPUT" ]] || die "无法自动确定地区，请重新运行并使用 -p/--province 指定"
}

interactive_selection() {
  local answer display_choice

  interactive_region_selection

  printf '\n%s测速档位%s\n' "$C_CYAN" "$C_RESET"
  printf '  1) 100 Mbps\n  2) 200 Mbps（默认）\n  3) 400 Mbps\n'
  printf '  请选择 [1/2/3]: '
  IFS= read -r answer || answer=""
  case "${answer:-2}" in
    1|100) SPEED_TARGET_MBPS=100 ;;
    2|200) SPEED_TARGET_MBPS=200 ;;
    3|400) SPEED_TARGET_MBPS=400 ;;
    *) die "无效的测速档位: $answer" ;;
  esac

  if [[ -z "$NODEQUALITY_URL" ]]; then
    printf '\n%s结果展示%s\n' "$C_CYAN" "$C_RESET"
    printf '  1) 独立测速结果（默认）\n  2) 关联已有 NodeQuality 报告\n'
    printf '  请选择 [1/2]: '
    IFS= read -r display_choice || display_choice=""
    case "${display_choice:-1}" in
      1) ;;
      2)
        printf '  NodeQuality 报告 URL: '
        IFS= read -r NODEQUALITY_URL || NODEQUALITY_URL=""
        [[ -n "$NODEQUALITY_URL" ]] || die "没有输入 NodeQuality 报告 URL"
        ;;
      *) die "无效的结果展示选项: $display_choice" ;;
    esac
  fi
}

prepare_speed_selection() {
  local should_interact=0
  local should_prompt_region=0
  local bsg_requested=0

  if [[ "${REGION_INPUT,,}" == "auto" ]]; then
    REGION_INPUT=""
  fi
  if [[ -n "$NODE_ROUTE_KEY" ]]; then
    resolve_node_route
    if [[ -z "$REGION_INPUT" ]]; then
      REGION_INPUT="$NODE_ROUTE_REGION"
      info "指定节点登记在 $(region_name "$NODE_ROUTE_REGION") ($NODE_ROUTE_REGION)，已自动采用该省份"
    fi
    if [[ "${REGION_INPUT//[[:space:]]/}" =~ ^([Bb][Ss][Gg]|北上广)$ ]]; then
      die "--node 只能使用节点登记的一个省份，不能与 bsg 一起使用"
    fi
    normalize_regions "$REGION_INPUT"
    if [[ "$SELECTED_REGION_CODES" != "$NODE_ROUTE_REGION" ]]; then
      die "--node 指定节点登记在 $(region_name "$NODE_ROUTE_REGION") ($NODE_ROUTE_REGION)，不能用于 -p $SELECTED_REGION_CODES"
    fi
  else
    if [[ "${REGION_INPUT//[[:space:]]/}" =~ ^([Bb][Ss][Gg]|北上广)$ ]]; then
      bsg_requested=1
    fi
    if ((ORIGINAL_ARG_COUNT == 0)) && [[ -t 0 ]]; then
      should_interact=1
    elif [[ -z "$REGION_INPUT" && -t 0 ]]; then
      should_prompt_region=1
    fi

    if [[ -z "$REGION_INPUT" || "$should_interact" -eq 1 || "$bsg_requested" -eq 1 ]]; then
      detect_auto_region || true
    fi
    if ((should_interact == 1)); then
      interactive_selection
    elif ((should_prompt_region == 1)); then
      interactive_region_selection
    elif [[ -z "$REGION_INPUT" ]]; then
      REGION_INPUT="$AUTO_REGION_CODE"
      [[ -n "$REGION_INPUT" ]] || die "无法自动确定 SSH 来源地区，请使用 -p/--province 指定"
      info "SSH 来源 $SSH_CLIENT_IP，自动选择 $AUTO_REGION_NAME ($AUTO_REGION_CODE)"
    elif ((bsg_requested == 1)); then
      [[ -n "$AUTO_REGION_CODE" ]] || \
        die "无法识别 SSH 来源省份，不能使用 bsg；请用 -p 明确列出最多 5 个省份"
      info "SSH 来源 $SSH_CLIENT_IP，bsg 将选择 $AUTO_REGION_NAME、北京、上海和广东并自动去重"
    fi

    normalize_regions "$REGION_INPUT"
  fi
  select_run_families
  if [[ -n "$NODE_ROUTE_KEY" ]]; then
    info "指定 SQ 节点: $NODE_ROUTE_CARRIER / $NODE_ROUTE_ACCESS_MODE；最高 ${NODE_ROUTE_MAX_MBPS} Mbps"
  fi
  info "测速省份: $SELECTED_POINTS；单线程；速度档位: ${SPEED_TARGET_MBPS} Mbps；IP: ${RUN_FAMILIES// / + }"
  show_traffic_preflight
}

resolve_node_route() {
  local response_file="$TEMP_DIR/node-route.json"
  local http_code="200"
  local valid_region_name=""
  local -a curl_family_args=()
  [[ "$REPORT_BASE" != "$REPORT_BASE_PLACEHOLDER" ]] \
    || die "--node 需要已配置社区节点服务的 SpeedQuality 入口"
  validate_https_url "$REPORT_BASE" || die "SpeedQuality 服务地址无效"
  if [[ -n "${SPEEDQUALITY_NODE_ROUTE_FILE:-}" ]]; then
    [[ -r "$SPEEDQUALITY_NODE_ROUTE_FILE" ]] || die "指定节点测试响应不可读"
    response_file="$SPEEDQUALITY_NODE_ROUTE_FILE"
  else
    command -v curl >/dev/null 2>&1 || die "缺少 curl"
    case "$IP_FAMILY_SELECTION" in
      v4) curl_family_args=(-4) ;;
      v6) curl_family_args=(-6) ;;
    esac
    http_code=$(curl "${curl_family_args[@]}" --proto '=https' --tlsv1.2 -sS --retry 1 \
      --connect-timeout 8 --max-time 30 -X POST \
      -H 'Content-Type: application/json' \
      --data-binary "{\"route_key\":\"$NODE_ROUTE_KEY\"}" \
      --write-out '%{http_code}' --output "$response_file" \
      "${REPORT_BASE%/}/api/nodes/resolve" || true)
  fi
  case "$http_code" in
    200) ;;
    404) die "Route Key 不存在，或节点已暂停/注销" ;;
    503) die "指定节点当前离线或健康检查未通过" ;;
    *) die "无法解析指定节点（HTTP ${http_code:-000}）" ;;
  esac
  NODE_ROUTE_REGION=$(json_string_field "$response_file" region)
  NODE_ROUTE_CARRIER=$(json_string_field "$response_file" carrier)
  NODE_ROUTE_ACCESS_MODE=$(json_string_field "$response_file" access_mode)
  NODE_ROUTE_MAX_MBPS=$(json_number_field "$response_file" max_mbps)
  grep -Eq '"v4"' "$response_file" && NODE_ROUTE_HAS_V4=1 || NODE_ROUTE_HAS_V4=0
  grep -Eq '"v6"' "$response_file" && NODE_ROUTE_HAS_V6=1 || NODE_ROUTE_HAS_V6=0
  valid_region_name=$(region_name "$NODE_ROUTE_REGION" 2>/dev/null || true)
  [[ -n "$valid_region_name" && "$NODE_ROUTE_CARRIER" =~ ^(ct|cu|cm)$ &&
        "$NODE_ROUTE_MAX_MBPS" =~ ^(100|200|400)$ ]] \
    || die "节点服务返回了无效的 Route Key 信息"
  ((NODE_ROUTE_HAS_V4 == 1 || NODE_ROUTE_HAS_V6 == 1)) \
    || die "指定节点没有登记可用的 IPv4 或 IPv6"
  ((SPEED_TARGET_MBPS <= NODE_ROUTE_MAX_MBPS)) \
    || die "指定节点最高支持 ${NODE_ROUTE_MAX_MBPS} Mbps，请降低 -s/--speed"
}

has_ipv4_connectivity() {
  case "${SPEEDQUALITY_HAS_IPV4:-}" in
    1|true|yes) return 0 ;;
    0|false|no) return 1 ;;
  esac
  command -v curl >/dev/null 2>&1 || return 1
  [[ "$REPORT_BASE" != "$REPORT_BASE_PLACEHOLDER" ]] || return 1
  validate_https_url "$REPORT_BASE" || return 1
  curl -4 --proto '=https' --tlsv1.2 -fsS --connect-timeout 3 --max-time 6 \
    "${REPORT_BASE%/}/health" -o /dev/null 2>/dev/null
}

has_ipv6_connectivity() {
  case "${SPEEDQUALITY_HAS_IPV6:-}" in
    1|true|yes) return 0 ;;
    0|false|no) return 1 ;;
  esac
  command -v curl >/dev/null 2>&1 || return 1
  [[ "$REPORT_BASE" != "$REPORT_BASE_PLACEHOLDER" ]] || return 1
  validate_https_url "$REPORT_BASE" || return 1
  curl -6 --proto '=https' --tlsv1.2 -fsS --connect-timeout 3 --max-time 6 \
    "${REPORT_BASE%/}/health" -o /dev/null 2>/dev/null
}

select_run_families() {
  local server_has_v4=0
  local server_has_v6=0

  case "$IP_FAMILY_SELECTION" in
    auto)
      if has_ipv4_connectivity; then server_has_v4=1; fi
      if has_ipv6_connectivity; then server_has_v6=1; fi
      ;;
    v4)
      if has_ipv4_connectivity; then server_has_v4=1; fi
      ;;
    v6)
      if has_ipv6_connectivity; then server_has_v6=1; fi
      ;;
  esac

  RUN_FAMILIES=""
  case "$IP_FAMILY_SELECTION" in
    auto)
      if ((server_has_v4 == 1)) && \
          { [[ -z "$NODE_ROUTE_KEY" ]] || ((NODE_ROUTE_HAS_V4 == 1)); }; then
        RUN_FAMILIES="v4"
      fi
      if ((server_has_v6 == 1)) && \
          { [[ -z "$NODE_ROUTE_KEY" ]] || ((NODE_ROUTE_HAS_V6 == 1)); }; then
        RUN_FAMILIES="${RUN_FAMILIES:+$RUN_FAMILIES }v6"
      fi
      if [[ -z "$RUN_FAMILIES" ]]; then
        if ((server_has_v4 == 0 && server_has_v6 == 0)); then
          die "当前服务器未检测到 IPv4 或 IPv6 连通性"
        fi
        die "当前服务器与指定节点没有共同可用的 IP 类型"
      fi
      case "$RUN_FAMILIES" in
        "v4 v6") info "检测到 IPv4 和 IPv6 连通性，默认测试两者" ;;
        v4) info "本次仅有可用的 IPv4，默认只测试 IPv4" ;;
        v6) info "本次仅有可用的 IPv6，默认只测试 IPv6" ;;
      esac
      ;;
    v4)
      ((server_has_v4 == 1)) \
        || die "已指定 -v4，但当前服务器未检测到 IPv4 连通性"
      if [[ -n "$NODE_ROUTE_KEY" ]] && ((NODE_ROUTE_HAS_V4 != 1)); then
        die "指定节点没有登记 IPv4，不能使用 -v4"
      fi
      RUN_FAMILIES="v4"
      info "已指定仅测试 IPv4"
      ;;
    v6)
      ((server_has_v6 == 1)) \
        || die "已指定 -v6，但当前服务器未检测到 IPv6 连通性"
      if [[ -n "$NODE_ROUTE_KEY" ]] && ((NODE_ROUTE_HAS_V6 != 1)); then
        die "指定节点没有登记 IPv6，不能使用 -v6"
      fi
      RUN_FAMILIES="v6"
      info "已指定仅测试 IPv6"
      ;;
  esac

  PRIMARY_FAMILY="${RUN_FAMILIES%% *}"
  if [[ "$PRIMARY_FAMILY" == "v6" ]]; then
    PRIMARY_CURL_FAMILY="-6"
  else
    PRIMARY_CURL_FAMILY="-4"
  fi
  if [[ " $RUN_FAMILIES " == *" v6 "* ]]; then
    IP_MODE="v6"
  else
    IP_MODE="v4"
  fi
}

parse_args() {
  ORIGINAL_ARG_COUNT=$#
  while (($# > 0)); do
    case "$1" in
      -p|--province)
        require_value "$1" "$#"
        REGION_INPUT="$2"
        shift 2
        ;;
      --province=*)
        REGION_INPUT="${1#*=}"
        shift
        ;;
      -s|--speed)
        require_value "$1" "$#"
        SPEED_TARGET_MBPS="$2"
        shift 2
        ;;
      --speed=*)
        SPEED_TARGET_MBPS="${1#*=}"
        shift
        ;;
      -v4|--ipv4)
        [[ "$IP_FAMILY_SELECTION" != "v6" ]] \
          || die "-v4 与 -v6 不能同时使用"
        IP_FAMILY_SELECTION="v4"
        shift
        ;;
      -v6|--ipv6)
        [[ "$IP_FAMILY_SELECTION" != "v4" ]] \
          || die "-v4 与 -v6 不能同时使用"
        IP_FAMILY_SELECTION="v6"
        shift
        ;;
      --nq)
        require_value "$1" "$#"
        NODEQUALITY_URL="$2"
        shift 2
        ;;
      --nq=*)
        NODEQUALITY_URL="${1#*=}"
        shift
        ;;
      --node)
        require_value "$1" "$#"
        NODE_ROUTE_KEY="$2"
        shift 2
        ;;
      --node=*)
        NODE_ROUTE_KEY="${1#*=}"
        shift
        ;;
      --nodequality|--nodequality=*|--with-nodequality|--nodequality-arg|--nodequality-arg=*|--max-time-gap|--max-time-gap=*|--result|--result=*|--no-result-file|--speed-result|--speed-result=*)
        die "参数 $1 已移除；NodeQuality 请使用 --nq URL，结果默认直接生成展示链接"
        ;;
      -l|--list-provinces)
        list_provinces
        exit 0
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      --version)
        printf 'SpeedQuality %s\nProbe %s\n' "$SPEEDQUALITY_VERSION" "$PROBE_VERSION"
        exit 0
        ;;
      --mode|--mode=*|--duration|--duration=*|-y|--yes|--interactive|-4|--v4|--no-ipv6|-6|--v6|-b|--dual|-n|-t|--target|--target=*|-r|--region|--regions|--points|--list-regions|-V|--all)
        die "参数 $1 已移除；请使用 --help 查看当前参数"
        ;;
      --)
        die "不支持透传测速器参数"
        ;;
      *)
        die "未知参数: $1"
        ;;
    esac
  done
  case "$SPEED_TARGET_MBPS" in
    100|200|400) ;;
    *) die "-s/--speed 只支持 100、200 或 400 Mbps" ;;
  esac
  if [[ -n "$NODE_ROUTE_KEY" && ! "$NODE_ROUTE_KEY" =~ ^sqn_[A-Za-z0-9_-]{24,96}$ ]]; then
    die "--node Route Key 格式无效"
  fi
}

normalize_nodequality_url() {
  local value="$1"
  local token

  value="${value%/}"
  if [[ "$value" =~ ^[A-Za-z0-9_-]{16,128}$ ]]; then
    printf 'https://nodequality.com/r/%s\n' "$value"
    return 0
  fi

  if [[ "$value" =~ ^https?://([Ww][Ww][Ww]\.)?([Nn][Oo][Dd][Ee][Qq][Uu][Aa][Ll][Ii][Tt][Yy])\.[Cc][Oo][Mm]/r/([A-Za-z0-9_-]{16,128})$ ]]; then
    token="${BASH_REMATCH[3]}"
    printf 'https://nodequality.com/r/%s\n' "$token"
    return 0
  fi

  return 1
}

validate_https_url() {
  local value="$1"
  local pattern='^https://[A-Za-z0-9._~:/?#@!$&*+,;=%-]+$'
  [[ "$value" =~ $pattern ]]
}

json_number_field() {
  local file="$1"
  local key="$2"
  sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p" "$file" | head -n 1
}

sha1_stdin() {
  if command -v sha1sum >/dev/null 2>&1; then
    sha1sum | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 1 | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha1 | awk '{print $NF}'
  else
    return 1
  fi
}

fetch_nodequality_record() {
  local token="${NODEQUALITY_URL##*/}"
  local info_url="${NODEQUALITY_API%/}/ipinfo"
  local record_url="${NODEQUALITY_API%/}/record/${token}"
  local current_ip timestamp signature

  NODEQUALITY_IPINFO_FILE="$TEMP_DIR/nodequality-ipinfo.json"
  NODEQUALITY_RECORD_FILE="$TEMP_DIR/nodequality-record.json"

  if [[ -n "${SPEEDQUALITY_NODEQUALITY_IPINFO_FILE:-}" || -n "${SPEEDQUALITY_NODEQUALITY_RECORD_FILE:-}" ]]; then
    [[ -r "${SPEEDQUALITY_NODEQUALITY_IPINFO_FILE:-}" ]] || return 1
    [[ -r "${SPEEDQUALITY_NODEQUALITY_RECORD_FILE:-}" ]] || return 1
    NODEQUALITY_IPINFO_FILE="$SPEEDQUALITY_NODEQUALITY_IPINFO_FILE"
    NODEQUALITY_RECORD_FILE="$SPEEDQUALITY_NODEQUALITY_RECORD_FILE"
    return 0
  fi

  validate_https_url "$NODEQUALITY_API" || return 1
  validate_https_url "$NODEQUALITY_ORIGIN" || return 1
  command -v curl >/dev/null 2>&1 || return 1
  if ! curl "$PRIMARY_CURL_FAMILY" --proto '=https' --tlsv1.2 -fsSL --retry 2 \
    --connect-timeout 8 --max-time 30 \
    -A "$NODEQUALITY_USER_AGENT" \
    -H "Origin: $NODEQUALITY_ORIGIN" -H "Referer: ${NODEQUALITY_ORIGIN%/}/" \
    "$info_url" -o "$NODEQUALITY_IPINFO_FILE"; then
    return 1
  fi

  current_ip=$(json_string_field "$NODEQUALITY_IPINFO_FILE" ip)
  timestamp=$(json_number_field "$NODEQUALITY_IPINFO_FILE" ts)
  [[ -n "$current_ip" && -n "$timestamp" ]] || return 1
  signature=$(printf 'GET\n\n%s\n\n%s\n\n%s%s\n\n' \
    "$record_url" "$NODEQUALITY_USER_AGENT" "$current_ip" "$timestamp" | sha1_stdin) || return 1
  [[ "$signature" =~ ^[A-Fa-f0-9]{40}$ ]] || return 1

  curl "$PRIMARY_CURL_FAMILY" --proto '=https' --tlsv1.2 -fsSL --retry 2 \
    --connect-timeout 8 --max-time 60 \
    -A "$NODEQUALITY_USER_AGENT" \
    -H "Origin: $NODEQUALITY_ORIGIN" -H "Referer: ${NODEQUALITY_ORIGIN%/}/" \
    -H "x-dynamic-sign-v: $signature" -H "x-dynamic-sign-t: $timestamp" \
    "$record_url" -o "$NODEQUALITY_RECORD_FILE"
}

analyze_nodequality_record() {
  "$PROBE_BINARY" nq-verify \
    --ipinfo "$NODEQUALITY_IPINFO_FILE" \
    --record "$NODEQUALITY_RECORD_FILE" \
    --tested-at "$SPEED_TEST_EPOCH" \
    --max-time-gap "$MAX_TIME_GAP_MINUTES" \
    --snapshot "$NODEQUALITY_SNAPSHOT_FILE"
}

format_time_gap() {
  local seconds="$1"
  local minutes days hours

  [[ "$seconds" =~ ^[0-9]+$ ]] || {
    printf '未知'
    return
  }
  minutes=$(((10#$seconds + 30) / 60))
  if ((minutes < 60)); then
    printf '%d 分钟' "$minutes"
  elif ((minutes < 1440)); then
    printf '%d 小时 %d 分钟' "$((minutes / 60))" "$((minutes % 60))"
  else
    days=$((minutes / 1440))
    hours=$(((minutes % 1440) / 60))
    printf '%d 天 %d 小时' "$days" "$hours"
  fi
}

has_verified_nodequality() {
  case "$BIND_STATUS" in
    verified|verified_stale|verified_time_unknown) return 0 ;;
    *) return 1 ;;
  esac
}

check_nodequality_binding_availability() {
  local configured="${SPEEDQUALITY_NQ_BINDING_ENABLED:-}"
  local response_file status

  case "${configured,,}" in
    0|false|off|no|disabled) NODEQUALITY_BINDING_ENABLED=0; return 0 ;;
    1|true|on|yes|enabled) NODEQUALITY_BINDING_ENABLED=1; return 0 ;;
    "") ;;
    *) warn "忽略无效的 SPEEDQUALITY_NQ_BINDING_ENABLED 值" ;;
  esac

  # Test and offline integrations can inject a session token without a live Worker.
  [[ -z "${SPEEDQUALITY_SESSION_TOKEN:-}" &&
     -z "${SPEEDQUALITY_SESSION_TOKEN_V4:-}" &&
     -z "${SPEEDQUALITY_SESSION_TOKEN_V6:-}" ]] || return 0
  [[ "$REPORT_BASE" != "$REPORT_BASE_PLACEHOLDER" ]] || return 0
  validate_https_url "$REPORT_BASE" || return 0
  command -v curl >/dev/null 2>&1 || return 0

  response_file="$TEMP_DIR/features.json"
  NODEQUALITY_BINDING_ENABLED=0
  status=$(curl "$PRIMARY_CURL_FAMILY" --proto '=https' --tlsv1.2 -sS --retry 1 \
    --connect-timeout 5 --max-time 10 --write-out '%{http_code}' \
    "${REPORT_BASE%/}/api/features" -o "$response_file" 2>/dev/null || true)
  if [[ "$status" == "200" ]] && \
      grep -Eq '"nodequality_binding"[[:space:]]*:[[:space:]]*true' "$response_file"; then
    NODEQUALITY_BINDING_ENABLED=1
  fi
}

bind_nodequality_result() {
  local analysis status current_ip current_asn report_ips report_asn reason
  local time_status report_time gap_seconds report_epoch time_source gap_display

  [[ -n "$NODEQUALITY_URL" ]] || return 0
  NODEQUALITY_SNAPSHOT_FILE="$TEMP_DIR/nodequality-snapshot.json"
  info "正在校验 NodeQuality 报告与当前服务器身份是否匹配"
  if ! fetch_nodequality_record; then
    warn "无法读取 NodeQuality 报告，已拒绝绑定；分享页将只包含测速结果"
    BIND_STATUS="unverified"
    return 0
  fi
  set +e
  analysis=$(analyze_nodequality_record)
  status=$?
  set -e
  if ((status != 0)) || [[ -z "$analysis" ]]; then
    warn "NodeQuality 报告解析失败，已拒绝绑定；分享页将只包含测速结果"
    BIND_STATUS="unverified"
    return 0
  fi

  IFS='|' read -r status current_ip current_asn report_ips report_asn reason \
    time_status report_time gap_seconds report_epoch time_source <<< "$analysis"
  NODEQUALITY_TIME_GAP_SECONDS="$gap_seconds"
  NODEQUALITY_REPORT_EPOCH="$report_epoch"
  NODEQUALITY_TIME_SOURCE="$time_source"
  NODEQUALITY_IDENTITY_REASON="$reason"
  case "$status" in
    match)
      case "$reason" in
        full_ip) success "服务器身份校验通过：完整 IP 一致（${current_ip}）" ;;
        masked_ip_and_asn) success "服务器身份校验通过：脱敏 IP 网段匹配且 ASN 一致（${report_ips} / AS${report_asn}）" ;;
        *) success "服务器身份校验通过：当前 ${current_ip} / AS${current_asn}，报告 ${report_ips} / AS${report_asn}" ;;
      esac
      case "$time_status" in
        close)
          gap_display=$(format_time_gap "$gap_seconds")
          success "时间校验通过：NodeQuality ${report_time}，与本次测速相差 $gap_display"
          ;;
        stale)
          gap_display=$(format_time_gap "$gap_seconds")
          alert "检测时间差过大：NodeQuality ${report_time}，与本次测速相差 $gap_display（阈值 ${MAX_TIME_GAP_MINUTES} 分钟）"
          alert "服务器身份匹配，仍会绑定；分享页中将高亮时间警告"
          ;;
        *)
          warn "无法确认 NodeQuality 与本次测速的时间差；仍会绑定并高亮时间警告"
          ;;
      esac
      case "$time_status" in
        close) BIND_STATUS="verified" ;;
        stale) BIND_STATUS="verified_stale" ;;
        *) BIND_STATUS="verified_time_unknown" ;;
      esac
      ;;
    mismatch)
      BIND_STATUS="mismatch"
      warn "NodeQuality 报告与当前服务器身份不匹配"
      warn "当前服务器: ${current_ip:-未知} / AS${current_asn:-未知}；报告: ${report_ips:-未知} / AS${report_asn:-未知}（$reason）"
      warn "已拒绝绑定，分享链接只包含本次测速结果"
      ;;
    *)
      BIND_STATUS="unverified"
      warn "NodeQuality 报告缺少可验证的服务器 IP 信息（$reason）"
      warn "已拒绝绑定，分享链接只包含本次测速结果"
      ;;
  esac
}

sha256_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    die "缺少 sha256sum 或 shasum，无法校验测速程序"
  fi
}

download_file() {
  local url="$1"
  local destination="$2"
  curl --proto '=https' --tlsv1.2 -fsSL --retry 3 --connect-timeout 15 --max-time 180 \
    "$url" -o "$destination"
}

platform_arch() {
  local os
  local arch
  os=$(uname -s)
  arch=$(uname -m)

  [[ "$os" == "Linux" ]] || die "当前只支持 Linux，检测到: $os"
  case "$arch" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) die "不支持的 CPU 架构: $arch" ;;
  esac
}

release_sha256() {
  local asset="$1"
  local sums_file="$TEMP_DIR/sha256sums.txt"
  local expected

  download_file "${PROBE_BASE%/}/checksums.txt" "$sums_file" \
    || die "无法下载 ${PROBE_VERSION} 的校验文件"
  expected=$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums_file")
  [[ "$expected" =~ ^[A-Fa-f0-9]{64}$ ]] || die "校验文件中没有找到 $asset"
  printf '%s\n' "${expected,,}"
}

resolve_expected_sha256() {
  local asset="$1"
  local expected="${SPEEDQUALITY_PROBE_SHA256:-}"

  if [[ -n "$expected" ]]; then
    [[ "$expected" =~ ^[A-Fa-f0-9]{64}$ ]] || die "SPEEDQUALITY_PROBE_SHA256 必须是 64 位十六进制值"
    printf '%s\n' "${expected,,}"
  else
    release_sha256 "$asset"
  fi
}

resolve_probe_binary() {
  local supplied="${SPEEDQUALITY_PROBE_BIN:-}"
  local arch
  local asset
  local expected
  local cached
  local staged
  local actual

  if [[ -n "$supplied" ]]; then
    [[ -f "$supplied" && -x "$supplied" ]] || die "SPEEDQUALITY_PROBE_BIN 不可执行: $supplied"
    printf '%s\n' "$supplied"
    return 0
  fi

  command -v curl >/dev/null 2>&1 || die "缺少 curl"
  [[ "$PROBE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "探测器版本不合法: $PROBE_VERSION"
  if [[ -z "$PROBE_BASE" ]]; then
    [[ "$REPORT_BASE" != "$REPORT_BASE_PLACEHOLDER" ]] \
      || die "当前脚本没有配置 SpeedQuality 服务地址"
    PROBE_BASE="${REPORT_BASE%/}/bin/${PROBE_VERSION}"
  fi
  validate_https_url "$PROBE_BASE" || die "探测器下载地址无效"

  arch=$(platform_arch)
  asset="sqprobe-linux-${arch}"
  expected=$(resolve_expected_sha256 "$asset")
  cached="${CACHE_DIR}/${PROBE_VERSION}/${asset}"

  if [[ -x "$cached" ]]; then
    actual=$(sha256_file "$cached")
    if [[ "${actual,,}" == "$expected" ]]; then
      printf '%s\n' "$cached"
      return 0
    fi
    warn "缓存中的测速程序校验失败，将重新下载"
  fi

  mkdir -p -- "$(dirname -- "$cached")"
  staged="$TEMP_DIR/$asset"
  info "下载 SpeedQuality 探测器 ${PROBE_VERSION} (${arch})"
  download_file "${PROBE_BASE%/}/${asset}" "$staged" \
    || die "测速程序下载失败"
  actual=$(sha256_file "$staged")
  [[ "${actual,,}" == "$expected" ]] \
    || die "测速程序 SHA-256 校验失败（期望 $expected，实际 ${actual,,}）"
  chmod 0755 "$staged"
  mv -f -- "$staged" "$cached"
  printf '%s\n' "$cached"
}

detect_traffic_interfaces() {
  local dev_file="${SPEEDQUALITY_NETDEV_FILE:-/proc/net/dev}"
  local candidates="" output="" iface

  [[ -r "$dev_file" ]] || return 1
  if [[ -n "${SPEEDQUALITY_NETWORK_INTERFACES:-}" ]]; then
    candidates="${SPEEDQUALITY_NETWORK_INTERFACES//,/ }"
  elif command -v ip >/dev/null 2>&1; then
    candidates=$(
      {
        ip -o -4 route show default 2>/dev/null || true
        ip -o -6 route show default 2>/dev/null || true
      } | awk '{for (i = 1; i <= NF; i++) if ($i == "dev" && $(i + 1) != "") print $(i + 1)}' \
        | awk '!seen[$0]++'
    )
  fi
  if [[ -z "$candidates" && -r /proc/net/route ]]; then
    candidates=$(awk 'NR > 1 && $2 == "00000000" {print $1}' /proc/net/route | awk '!seen[$0]++')
  fi
  if [[ -z "$candidates" ]]; then
    candidates=$(awk -F: 'NR > 2 {gsub(/[[:space:]]/, "", $1); if ($1 != "lo") print $1}' "$dev_file")
  fi

  for iface in $candidates; do
    iface="${iface%%@*}"
    [[ "$iface" != "lo" && "$iface" =~ ^[A-Za-z0-9_.:-]+$ ]] || continue
    awk -F: -v wanted="$iface" '
      {name = $1; gsub(/[[:space:]]/, "", name)}
      name == wanted {found = 1}
      END {exit found ? 0 : 1}
    ' "$dev_file" || continue
    case " $output " in
      *" $iface "*) ;;
      *) output="${output:+$output }$iface" ;;
    esac
  done
  [[ -n "$output" ]] || return 1
  printf '%s\n' "$output"
}

read_network_counters() {
  local dev_file="${SPEEDQUALITY_NETDEV_FILE:-/proc/net/dev}"
  local iface stats selected rx tx
  local -a fields
  local rx_total=0 tx_total=0 matched=0 selected_match

  [[ -n "$TRAFFIC_INTERFACES" && -r "$dev_file" ]] || return 1
  while IFS=: read -r iface stats; do
    iface="${iface//[[:space:]]/}"
    selected_match=0
    for selected in $TRAFFIC_INTERFACES; do
      if [[ "$iface" == "$selected" ]]; then
        selected_match=1
        break
      fi
    done
    ((selected_match == 1)) || continue
    read -r -a fields <<< "$stats"
    rx="${fields[0]:-}"
    tx="${fields[8]:-}"
    [[ "$rx" =~ ^[0-9]+$ && "$tx" =~ ^[0-9]+$ ]] || continue
    rx_total=$((rx_total + 10#$rx))
    tx_total=$((tx_total + 10#$tx))
    matched=$((matched + 1))
  done < "$dev_file"
  ((matched > 0)) || return 1
  printf '%s|%s\n' "$rx_total" "$tx_total"
}

finish_traffic_measurement() {
  local before="$1" after before_rx before_tx after_rx after_tx interface_display
  [[ -n "$before" ]] || {
    warn "无法读取默认出口网卡计数器，本次不显示实际流量"
    return 0
  }
  after=$(read_network_counters || true)
  [[ -n "$after" ]] || {
    warn "测速结束后无法读取网卡计数器，本次不显示实际流量"
    return 0
  }
  IFS='|' read -r before_rx before_tx <<< "$before"
  IFS='|' read -r after_rx after_tx <<< "$after"
  if ((after_rx < before_rx || after_tx < before_tx)); then
    warn "测速期间网卡计数器发生重置，本次不显示实际流量"
    return 0
  fi
  TRAFFIC_RX_BYTES=$((after_rx - before_rx))
  TRAFFIC_TX_BYTES=$((after_tx - before_tx))
  TRAFFIC_TOTAL_BYTES=$((TRAFFIC_RX_BYTES + TRAFFIC_TX_BYTES))
  interface_display="${TRAFFIC_INTERFACES// /, }"
  success "实际流量: 下载 $(format_bytes "$TRAFFIC_RX_BYTES")，上传 $(format_bytes "$TRAFFIC_TX_BYTES")，合计 $(format_bytes "$TRAFFIC_TOTAL_BYTES")"
  info "统计接口: $interface_display；数值为测速期间网卡差值，可能包含同期其它进程流量"
}

request_speed_session_token() {
  local curl_family="$1"
  local response_file="$2"
  local -a node_route_args=()
  if [[ -n "$NODE_ROUTE_KEY" ]]; then
    node_route_args=(--data-urlencode "node_route=$NODE_ROUTE_KEY")
  fi
  curl "$curl_family" --proto '=https' --tlsv1.2 -fsS --retry 2 \
    --connect-timeout 8 --max-time 30 \
    -X POST \
    -H 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode "regions=$SELECTED_REGION_CODES" \
    --data-urlencode "mode=$SPEED_MODE" \
    --data-urlencode "ip_mode=$IP_MODE" \
    --data-urlencode "duration_seconds=$SPEED_DURATION_SECONDS" \
    --data-urlencode "target_mbps=$SPEED_TARGET_MBPS" \
    "${node_route_args[@]}" \
    "${REPORT_BASE%/}/api/session" -o "$response_file"
}

create_speed_session() {
  local response_file_v4="$TEMP_DIR/session-token-v4.txt"
  local response_file_v6="$TEMP_DIR/session-token-v6.txt"
  local injected_token="${SPEEDQUALITY_SESSION_TOKEN:-}"
  if [[ -n "$injected_token" || -n "${SPEEDQUALITY_SESSION_TOKEN_V4:-}" ||
        -n "${SPEEDQUALITY_SESSION_TOKEN_V6:-}" ]]; then
    if [[ " $RUN_FAMILIES " == *" v4 "* ]]; then
      SESSION_TOKEN_V4="${SPEEDQUALITY_SESSION_TOKEN_V4:-$injected_token}"
    fi
    if [[ " $RUN_FAMILIES " == *" v6 "* ]]; then
      SESSION_TOKEN_V6="${SPEEDQUALITY_SESSION_TOKEN_V6:-$injected_token}"
    fi
  else
    [[ "$REPORT_BASE" != "$REPORT_BASE_PLACEHOLDER" ]] \
      || die "当前脚本没有配置 SpeedQuality 服务地址"
    validate_https_url "$REPORT_BASE" || die "SpeedQuality 服务地址无效"
    if [[ " $RUN_FAMILIES " == *" v4 "* ]]; then
      if ! request_speed_session_token -4 "$response_file_v4"; then
        die "无法通过 IPv4 创建来源绑定测速会话"
      fi
      SESSION_TOKEN_V4=$(tr -d '\r\n' < "$response_file_v4")
    fi
    if [[ " $RUN_FAMILIES " == *" v6 "* ]]; then
      if ! request_speed_session_token -6 "$response_file_v6"; then
        die "无法通过 IPv6 创建来源绑定测速会话"
      fi
      SESSION_TOKEN_V6=$(tr -d '\r\n' < "$response_file_v6")
    fi
  fi
  if [[ " $RUN_FAMILIES " == *" v4 "* ]]; then
    [[ "$SESSION_TOKEN_V4" =~ ^[A-Za-z0-9_-]{32,128}$ ]] \
      || die "测速服务返回了无效 IPv4 会话"
  fi
  if [[ " $RUN_FAMILIES " == *" v6 "* ]]; then
    [[ "$SESSION_TOKEN_V6" =~ ^[A-Za-z0-9_-]{32,128}$ ]] \
      || die "测速服务返回了无效 IPv6 会话"
  fi
  if [[ "$PRIMARY_FAMILY" == "v6" ]]; then
    SESSION_TOKEN="$SESSION_TOKEN_V6"
  else
    SESSION_TOKEN="$SESSION_TOKEN_V4"
  fi
}

request_node_lease() {
  local region="$1"
  local family="$2"
  local destination="$3"
  local fixture
  local curl_family="-4"
  local session_token="$SESSION_TOKEN_V4"
  local preparation=""
  local status=""
  local poll
  local -a preparation_args=()
  if [[ -n "${SPEEDQUALITY_LEASE_DIR:-}" ]]; then
    fixture="${SPEEDQUALITY_LEASE_DIR%/}/${region}-${family}.json"
    if [[ ! -r "$fixture" ]]; then
      fixture="${SPEEDQUALITY_LEASE_DIR%/}/default-${family}.json"
    fi
    [[ -r "$fixture" ]] || return 1
    cp -- "$fixture" "$destination"
    return 0
  fi
  if [[ "$family" == "v6" ]]; then
    curl_family="-6"
    session_token="$SESSION_TOKEN_V6"
  fi
  for ((poll = 1; poll <= 20; poll++)); do
    preparation_args=()
    if [[ -n "$preparation" ]]; then
      preparation_args=(--data-urlencode "preparation=$preparation")
    fi
    status=$(curl "$curl_family" --proto '=https' --tlsv1.2 -sS \
      --connect-timeout 8 --max-time 30 \
      --write-out '%{http_code}' \
      -X POST \
      -H "Authorization: Bearer $session_token" \
      -H 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode "region=$region" \
      --data-urlencode "family=$family" \
      "${preparation_args[@]}" \
      "${REPORT_BASE%/}/api/node-lease" -o "$destination") || return 1
    if [[ "$status" == "200" ]]; then
      return 0
    fi
    if [[ "$status" != "202" ]]; then
      return 1
    fi
    preparation=$(sed -n 's/.*"preparation"[[:space:]]*:[[:space:]]*"\([A-Za-z0-9_-]*\)".*/\1/p' \
      "$destination" | head -n 1)
    [[ "$preparation" =~ ^lease_[A-Za-z0-9_-]{16,80}$ ]] || return 1
    sleep 1
  done
  return 1
}

run_speedtest() {
  local status attempt region family lease_file attempt_result
  local -a diagnostic_args
  local traffic_before=""
  local completed=0 failed=0
  local -a region_codes

  PROBE_BINARY=$(resolve_probe_binary)
  create_speed_session
  SPEED_LOG="$TEMP_DIR/speedquality.log"
  SPEED_DATA_FILE="$TEMP_DIR/speedquality-results.ndjson"
  : > "$SPEED_LOG"
  : > "$SPEED_DATA_FILE"
  diagnostic_args=()
  if [[ -n "$SPEED_DIAGNOSTIC_LOG" ]]; then
    mkdir -p -- "$(dirname -- "$SPEED_DIAGNOSTIC_LOG")" 2>/dev/null || die "无法创建诊断日志目录"
    touch -- "$SPEED_DIAGNOSTIC_LOG" 2>/dev/null || die "无法写入诊断日志：$SPEED_DIAGNOSTIC_LOG"
    chmod 600 -- "$SPEED_DIAGNOSTIC_LOG" 2>/dev/null || true
    diagnostic_args=(--diagnostic-log "$SPEED_DIAGNOSTIC_LOG")
    info "诊断事件将写入 $SPEED_DIAGNOSTIC_LOG"
  fi
  TRAFFIC_INTERFACES=$(detect_traffic_interfaces || true)
  if [[ -n "$TRAFFIC_INTERFACES" ]]; then
    traffic_before=$(read_network_counters || true)
  fi
  IFS=',' read -r -a region_codes <<< "$SELECTED_REGION_CODES"
  info "开始运行 SpeedQuality 测速"
  for region in "${region_codes[@]}"; do
    for family in $RUN_FAMILIES; do
      status=1
      for attempt in 1 2; do
        lease_file="$TEMP_DIR/lease-${region}-${family}-${attempt}.json"
        attempt_result="$TEMP_DIR/result-${region}-${family}-${attempt}.ndjson"
        if ! request_node_lease "$region" "$family" "$lease_file"; then
          warn "${region}/${family} 获取节点失败（第 ${attempt} 次）"
          continue
        fi
        set +e
        "$PROBE_BINARY" --lease "$lease_file" --output "$attempt_result" "${diagnostic_args[@]}" | tee -a "$SPEED_LOG"
        status=${PIPESTATUS[0]}
        set -e
        if ((status == 0)); then
          cat "$attempt_result" >> "$SPEED_DATA_FILE"
          completed=$((completed + 1))
        elif [[ -s "$attempt_result" ]]; then
          cat "$attempt_result" >> "$SPEED_DATA_FILE"
        fi
        if ((status != 0)); then
          warn "${region}/${family} 测速失败；为避免重复消耗流量，本次不重新执行完整测速"
        fi
        break
      done
      if ((status != 0)); then
        failed=$((failed + 1))
      fi
    done
  done

  finish_traffic_measurement "$traffic_before"
  ((completed > 0)) || die "所有测速任务均失败"
  if ((failed > 0)); then
    warn "$failed 个地区/IP 类型测速失败，报告会保留失败状态"
  fi
  SPEED_TEST_EPOCH=$(date +%s)
}

clean_log() {
  local source="$1"
  LC_ALL=C tr '\r' '\n' < "$source" \
    | sed -E $'s/\033\\[[0-9;?]*[ -\\/]*[@-~]//g' \
    | awk 'BEGIN { blank=0 } /^[[:space:]]*$/ { if (!blank) print; blank=1; next } { print; blank=0 }'
}

publish_result() {
  local response=""
  local speed_text=""
  local response_file="$TEMP_DIR/report-response.txt"
  local clean_file="$TEMP_DIR/speedquality-clean.log"
  local clean_limited="$TEMP_DIR/speedquality-clean-limited.log"
  local snapshot_size=0
  local use_snapshot=0
  local -a form auth_args

  DISPLAY_URL=""
  if [[ -z "$REPORT_BASE" || "$REPORT_BASE" == "$REPORT_BASE_PLACEHOLDER" ]]; then
    warn "当前入口未配置分享服务，测速结果只显示在终端"
    return 0
  fi
  if ! validate_https_url "$REPORT_BASE"; then
    warn "分享服务地址无效，测速结果只显示在终端"
    return 0
  fi
  if [[ -n "$SESSION_TOKEN" ]]; then
    auth_args=(-H "Authorization: Bearer $SESSION_TOKEN")
  fi

  : > "$clean_limited"
  if [[ -n "$SPEED_LOG" && -s "$SPEED_LOG" ]]; then
    clean_log "$SPEED_LOG" > "$clean_file"
    head -c 16000 "$clean_file" > "$clean_limited"
  fi

  if has_verified_nodequality && [[ -s "$NODEQUALITY_SNAPSHOT_FILE" ]]; then
    snapshot_size=$(wc -c < "$NODEQUALITY_SNAPSHOT_FILE")
    if ((snapshot_size <= 98304)); then
      use_snapshot=1
      success "已生成安全的 NodeQuality 展示快照"
    else
      warn "NodeQuality 展示快照超过大小限制，将只保留原报告链接"
    fi
  fi

  if ((use_snapshot == 1)); then
    speed_text=$(<"$clean_limited")
    form=(
      --form-string "tested_at=$SPEED_TEST_EPOCH"
      --form-string "regions=$SELECTED_POINTS"
      --form-string "mode=$SPEED_MODE"
      --form-string "ip_mode=$IP_MODE"
      --form-string "speed_url="
      --form-string "speed_text=$speed_text"
      --form-string "duration_seconds=$SPEED_DURATION_SECONDS"
      --form-string "target_mbps=$SPEED_TARGET_MBPS"
      --form-string "traffic_rx_bytes=$TRAFFIC_RX_BYTES"
      --form-string "traffic_tx_bytes=$TRAFFIC_TX_BYTES"
      --form-string "bind_status=$BIND_STATUS"
      --form-string "version=$SPEEDQUALITY_VERSION"
      --form-string "nq_url=$NODEQUALITY_URL"
      --form-string "nq_tested_at=$NODEQUALITY_REPORT_EPOCH"
      --form-string "nq_time_source=$NODEQUALITY_TIME_SOURCE"
      --form-string "time_gap_seconds=$NODEQUALITY_TIME_GAP_SECONDS"
      --form-string "nq_identity_reason=$NODEQUALITY_IDENTITY_REASON"
      --form "nq_snapshot=@$NODEQUALITY_SNAPSHOT_FILE;type=application/json"
    )
    if [[ -s "$SPEED_DATA_FILE" ]]; then
      form+=(--form "speed_data=<$SPEED_DATA_FILE")
    fi
  else
    form=(
      --data-urlencode "tested_at=$SPEED_TEST_EPOCH"
      --data-urlencode "regions=$SELECTED_POINTS"
      --data-urlencode "mode=$SPEED_MODE"
      --data-urlencode "ip_mode=$IP_MODE"
      --data-urlencode "speed_url="
      --data-urlencode "speed_text@$clean_limited"
      --data-urlencode "duration_seconds=$SPEED_DURATION_SECONDS"
      --data-urlencode "target_mbps=$SPEED_TARGET_MBPS"
      --data-urlencode "traffic_rx_bytes=$TRAFFIC_RX_BYTES"
      --data-urlencode "traffic_tx_bytes=$TRAFFIC_TX_BYTES"
      --data-urlencode "bind_status=$BIND_STATUS"
      --data-urlencode "version=$SPEEDQUALITY_VERSION"
    )
    if [[ -s "$SPEED_DATA_FILE" ]]; then
      form+=(--data-urlencode "speed_data@$SPEED_DATA_FILE")
    fi
    if has_verified_nodequality; then
      form+=(
        --data-urlencode "nq_url=$NODEQUALITY_URL"
        --data-urlencode "nq_tested_at=$NODEQUALITY_REPORT_EPOCH"
        --data-urlencode "nq_time_source=$NODEQUALITY_TIME_SOURCE"
        --data-urlencode "time_gap_seconds=$NODEQUALITY_TIME_GAP_SECONDS"
        --data-urlencode "nq_identity_reason=$NODEQUALITY_IDENTITY_REASON"
      )
    fi
  fi

  if [[ -n "${SPEEDQUALITY_REPORT_RESPONSE_FILE:-}" ]]; then
    if [[ ! -r "$SPEEDQUALITY_REPORT_RESPONSE_FILE" ]]; then
      warn "分享服务测试响应不可读，测速结果只显示在终端"
      return 0
    fi
    response=$(<"$SPEEDQUALITY_REPORT_RESPONSE_FILE")
  else
    command -v curl >/dev/null 2>&1 || {
      warn "缺少 curl，无法生成分享链接"
      return 0
    }
    if ! curl "$PRIMARY_CURL_FAMILY" --proto '=https' --tlsv1.2 -fsS --retry 2 \
      --connect-timeout 8 --max-time 30 -H 'Accept: text/plain' \
      -X POST "${auth_args[@]}" "${form[@]}" "${REPORT_BASE%/}/api/results" -o "$response_file"; then
      warn "分享链接生成失败，测速结果只显示在终端"
      return 0
    fi
    response=$(<"$response_file")
  fi

  response="${response//$'\r'/}"
  response="${response//$'\n'/}"
  if validate_https_url "$response" && [[ "$response" == "${REPORT_BASE%/}/r/"* ]]; then
    RESULT_PAGE_URL="$response"
    DISPLAY_URL="$response"
    success "分享报告已生成: $RESULT_PAGE_URL"
  else
    warn "分享服务返回了无效链接，测速结果只显示在终端"
  fi
}

print_summary() {
  printf '\n%s测试完成%s\n' "$C_CYAN" "$C_RESET"
  if has_verified_nodequality; then
    printf '展示类型: SpeedQuality + NodeQuality 关联报告\n'
  else
    printf '展示类型: SpeedQuality 独立结果\n'
  fi
  if [[ "$TRAFFIC_TOTAL_BYTES" =~ ^[0-9]+$ ]]; then
    printf '实际流量: 下载 %s，上传 %s，合计 %s\n' \
      "$(format_bytes "$TRAFFIC_RX_BYTES")" \
      "$(format_bytes "$TRAFFIC_TX_BYTES")" \
      "$(format_bytes "$TRAFFIC_TOTAL_BYTES")"
  fi
  if [[ -n "$DISPLAY_URL" ]]; then
    printf '展示链接: %s\n' "$DISPLAY_URL"
  else
    printf '展示链接: 未生成，测速结果已显示在终端\n'
  fi
}

main() {
  parse_args "$@"

  TEMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/speedquality.XXXXXX")
  : > "$TEMP_DIR/$TEMP_MARKER_NAME"
  prepare_speed_selection

  if [[ -n "$NODEQUALITY_URL" ]]; then
    NODEQUALITY_URL=$(normalize_nodequality_url "$NODEQUALITY_URL") \
      || die "NodeQuality 链接无效，应为 https://nodequality.com/r/<token>"
    check_nodequality_binding_availability
    if ((NODEQUALITY_BINDING_ENABLED == 0)); then
      warn "SpeedQuality 当前已暂停 NodeQuality 关联；本次将继续测速并生成独立 SQ 报告"
      NODEQUALITY_URL=""
    fi
  fi

  run_speedtest

  if [[ -n "$NODEQUALITY_URL" ]]; then
    bind_nodequality_result
  fi

  publish_result
  print_summary
}

main "$@"
