#!/usr/bin/env bash
# libyunxcore 原生库构建脚本（Linux；Windows 见 dll/windows-dll-kit/build-dll.ps1，参数口径一致）
#
# 尺寸优化结论（2026-11-06 两轮实测，62.98MB 基线）：
#   -Os                                → 40.37MB（-36%，本脚本采用；构建还更快 42.8s vs 67s）
#   去掉 --enable-all-security-services → 无损（TLS/https 经 agent 元数据+默认已可用，
#                                       go-loader YUNX_HTTPS_PROBE=1 真网络验证 update_check ok）
#   strip                              → 0 收益（产物本就无符号表）
#   UPX                                → 未采用（dlopen 场景有风险；分发用 xz -9 → 11.9MB）
#   Go 壳 -ldflags="-s -w"             → 17.3MB → 13.3MB（见文末）
# 二轮（build report 实测，均为 0 收益，勿再试）：
#   -H:IncludeLocales=en（locale 数据本就未打包）；-Wl,--gc-sections（链接参数不生效）；
#   -H:+UseCompactStrings（25 无此选项，紧凑字符串默认开启）；反射收窄（agent 元数据本就近空，
#   实际生效的是 reachability-metadata.json 统一新格式）
# 现状构成（size 命令）：text 16.8MB（-Os 已从 35.3 砍半）+ data 23.4MB（镜像堆快照：
#   byte[] 合计 ~7.1MB + String 数据 4.9MB + java.lang 实例 4MB——JDK/Kotlin/OkHttp 栈的
#   构建期初始化状态，无参数可动）。再往下只能动刀：Substitution/裁依赖（kotlin-stdlib、
#   coroutines、okhttp、sqlite、mp4parser 全是承重墙，风险大于收益）→ 40.4MB 就是当前实用底线。
# 三轮（用户建议的三条路径，全部证据关闭）：
#   AWT：strings 里确有 java2d 签名，但可达代码里 imageio+AWT+字体链合计仅 958KB
#        （-Os 后），来源是 core 的 MotionPhotoWriter（动图照片写入，上游功能）；
#        切除需 stub 上游功能换 2.4%，判定不值
#   镜像堆审计（-H:+PrintClassInitialization → reports/*.csv）：构建期初始化 8381 类，
#        绝大多数标注 "Core JDK classes are initialized at build time"（security/ssl/xml/
#        charset/timezone 全家桶 = native-image 硬策略，run_time 覆盖不受支持会坏 TLS）
#   byte[] 7.1MB 来源：资源已证伪（publicsuffix 仅 1.5KB、sqlite 原生库 ~1MB，
#        其余为 JDK 内部表）；-H:ExcludeResources 无目标可打
#   Xerces 链 ~1.6MB：定性为功能刚需（java.util.prefs 的 XML 后端是 JDK 设计）——prefs 由
#        我们 M6 二批的「与原版设置互通」工厂主动引入（DllFacade/SettingsRepository/
#        GitHubTokenStore/AnnouncementReadStore 等），换掉 = 失去与 Linux 移植版/原版的
#        设置实时互通（用户双开两个应用共享设置），判定不可动。动图项按决策不做。
# 报告生成：--emit build-report（HTML 内嵌数据在最大 <script> 块；--emit 模式会顺带倒出
#   JDK 辅助 .so 如 libawt，那不是镜像内容，勿被误导）
set -euo pipefail
cd "$(dirname "$0")/.."

XERIAL="${XERIAL:-$HOME/.local/share/yunx-desktop/jars/sqlite-jdbc-3.47.1.0.jar}"
SDK_JAR="${SDK_JAR:-$HOME/.local/share/yunx-desktop/sdk-api.jar}"

mise exec -- gradle -q :dll:compileKotlin :dll:compileJava :dll:processResources -PgraalvmSdkJar="$SDK_JAR"
CP="$XERIAL:dll/build/classes/java/main:dll/build/classes/kotlin/main:dll/build/resources/main:$(mise exec -- gradle -q :dll:printRuntimeClasspath | grep RUNTIME_CP= | cut -d= -f2-)"
mise x java@oracle-graalvm-25.0.5 -- native-image -J-Xmx6g -cp "$CP" \
  --shared -Os -H:Name=libyunxcore -H:Path=dll/build/native \
  --no-fallback -H:MaxDuplicationFactor=100

echo "== 构建完成 =="
ls -la dll/build/native/libyunxcore.so
echo "== 冒烟（本地 http 全链 + 可选 https 探针）=="
(cd dll/go-loader && go build -o loader . && ./loader && YUNX_HTTPS_PROBE=1 ./loader 2>&1 | grep -A1 'update_check(https)')
echo "== Go 壳 release 构建 =="
echo '  go build -trimpath -ldflags="-s -w" -o bin/yunx .   # 仓库根执行；17.3MB → 13.2MB'
echo '  可选 UPX（独立 exe 比 .so 安全；实测启动正常）：upx --best --lzma bin/yunx-demo  # → 4.1MB（31%）'
echo '  ★ Windows 分发默认不 UPX（杀软误报常见）；bin/ 已 gitignore，二进制不入库'
