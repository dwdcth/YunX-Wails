package com.yunx.dll;

import org.graalvm.nativeimage.IsolateThread;
import org.graalvm.nativeimage.c.function.CEntryPoint;
import org.graalvm.nativeimage.c.type.CCharPointer;
import org.graalvm.nativeimage.c.type.CTypeConversion;

/**
 * C ABI 导出层（Java 写，保证 @CEntryPoint 静态方法形态无歧义）。
 *
 * 宿主约定（与切片验证一致）：进程内只创建一个 isolate 并终生持有，
 * 退出即回收——不要在进程内 tear_down_isolate（实测不可靠）。
 */
public final class NativeExports {

    private NativeExports() {
    }

    /** 连通性探针：恒返回 42。 */
    @CEntryPoint(name = "yunx_ping")
    public static int ping(IsolateThread thread) {
        return 42;
    }

    /**
     * 执行一条 JSON 命令（见 DllFacade）。
     *
     * @param requestJson 请求 JSON（UTF-8，NUL 结尾）
     * @param outBuf      调用方提供的响应缓冲区
     * @param outLen      缓冲区长度（字节）
     * @return 写入的字节数（不含 NUL）；缓冲区不足返回 -1
     */
    @CEntryPoint(name = "yunx_call")
    public static int call(IsolateThread thread, CCharPointer requestJson, CCharPointer outBuf, int outLen) {
        String request = CTypeConversion.toJavaString(requestJson);
        String response = DllFacade.INSTANCE.handle(request);
        byte[] bytes = response.getBytes(java.nio.charset.StandardCharsets.UTF_8);
        if (bytes.length + 1 > outLen) {
            return -1;
        }
        for (int i = 0; i < bytes.length; i++) {
            outBuf.write(i, bytes[i]);
        }
        outBuf.write(bytes.length, (byte) 0);
        return bytes.length;
    }
}
