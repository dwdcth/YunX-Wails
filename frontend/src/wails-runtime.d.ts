// Wails v3 运行时注入模块的类型桩（vue-tsc 经 tsconfig paths 映射到此处；运行时由 Wails 提供）
export const Events: {
  On(name: string, callback: (data: unknown) => void): () => void
  Off(name: string): void
  Emit(name: string, data?: unknown): void
}
