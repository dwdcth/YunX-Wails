# Windows 实编 DLL 工具包

在 Windows 上把 `:core` + `:dll` 门面编成真正的 `yunxcore.dll` 并冒烟验证。
全部命令与 Linux 沙箱里已跑通的一套一一对应（见文末实测基线），只有工具链是 Windows 的。

## 前置

| 需要 | 说明 |
| --- | --- |
| GraalVM 21+（含 Native Image） | 设 `GRAALVM_HOME` 指向它（如 `C:\graalvm-jdk-21.x`） |
| Visual Studio 2022 +「使用 C++ 的桌面开发」 | native-image 在 Windows 上用 MSVC 链接器；脚本经 vswhere 自动定位 vcvars64 |
| JDK 17 | Gradle toolchain 用（GraalVM 21 本身是 JDK 21，toolchain 会另找 17；没有时先跑仓库的 `jdk-setup.ps1`） |
| Go 1.23+ | 只为冒烟加载器（可 `-SkipSmoke` 跳过）；Wails 3 正式构建需 Go 1.26+ |
| Gradle | 本机有就用；没有时脚本用仓库 wrapper jar 拉起（仓库未附 gradlew 脚本） |

## 一键跑

```powershell
$env:GRAALVM_HOME = "C:\graalvm-jdk-21.0.2"   # 按你的安装路径改
.\dll\windows-dll-kit\build-dll.ps1
```

脚本依次做：前置检查 → `gradle :dll:compileKotlin :dll:compileJava :dll:printRuntimeClasspath`
→ `native-image --shared` 产出 `dll\build\native\yunxcore.dll`（含 `.lib` 导入库与头文件）
→ dumpbin 校验导出 `yunx_ping` / `yunx_call` / `graal_create_isolate`
→ 编 `dll\go-loader` 并运行冒烟（期望输出见文末基线，Windows 版命令同理）。

## 提取 sdk-api.jar（仅 GraalVM 21 且发行包不带时）

编译 `@CEntryPoint` 需要 `org.graalvm.nativeimage` 等 API 类。GraalVM 23+ 的
`lib\graalvm\sdk-api.jar` 若存在，脚本会自动用；21 的发行包不带，按下面从 jimage 提取一份
（这正是 Linux 沙箱里验证切片用的同一办法），再设 `YUNX_SDK_JAR` 指向它：

```powershell
$gv = $env:GRAALVM_HOME
& "$gv\bin\jimage.exe" extract --dir "$env:TEMP\yunx-jimage" "$gv\lib\modules"
# 只需要 org.graalvm.nativeimage / org.graalvm.word / org.graalvm.collections 三个模块的类，
# 把它们拷到一个目录、删掉 module-info.class 后打包：
#   jar --create --file sdk-api.jar -C <目录> .
$env:YUNX_SDK_JAR = "C:\path\to\sdk-api.jar"
```

注意：这个 jar **只能**进编译 classpath，**绝不能**进 native-image 的 classpath
（它带的 META-INF/native-image 属性会让构建以 ForceOnModulePath 失败——实测踩过）。

## 实测基线（Linux 沙箱，2026-11-05，同一套命令）

- `:core:compileKotlin`、`:desktop:compileKotlin`、`:dll` 编译均 BUILD SUCCESSFUL
- native-image 构建 2 分 50 秒，产物 `libyunxcore.so` 22.8 MB（Windows 上为 `yunxcore.dll`，量级相近）
- Gradle 首次跑通注意：daemon 与客户端的回环连接在本沙箱需 `-Djava.net.preferIPv4Stack=true`
  （沙箱 IPv6 怪癖，普通 Windows 机器不需要）
- Go 冒烟输出（节选）：

```
PING 42
REQ {"cmd":"version"}                       → {"ok":true,"version":"1.3.3","preview":false}
REQ parse_share 夸克文案                     → {"found":true,"platform":"QUARK","shareId":"abc123XYZ","pwd":"a1b2"}
REQ parse_share 百度链接                     → {"found":true,"platform":"BAIDU","shareId":"qwertyuiop","pwd":"zz99"}
REQ parse_share 无链接文字                   → {"found":false}
REQ kv_put / kv_get                         → stored=true / value=hello（DLL 内文件版存储）
REQ key_roundtrip                           → {"consistent":true,"keyBytes":32}（真实 DbKeyStore）
REQ 未知命令                                 → {"ok":false,"error":"unknown cmd: nope"}
LOADER_DONE，exit=0
```

## 已知约束（都已写进代码注释）

- isolate 终生制：进程内只建一个、退出回收；不要 `graal_tear_down_isolate`。
- isolate 绑 OS 线程：Go 侧必须经 `yunxcore` 的专用线程封装调用（跨线程调用实测致命崩溃）。
- DLL 内的 `MachineProtector` 未注册时，DbKeyStore 的普通/严格档不可用（宽松档正常）；
  产品化时二选一：Wails 宿主经回调提供 DPAPI，或 DLL 在 Windows 上直接调 Crypt32（后续迭代）。
- DLL 内网络命令在普通 Windows 上无沙箱 IPv6 问题；扩展网络命令时无需再处理 preferIPv4Stack。
