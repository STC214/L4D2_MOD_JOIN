# L4D2 MOD 合并检查报告

## 结果

- 输入：`workshop/` 中 71 个 VPK，约 2.276 GiB，共 7023 个文件。
- 输出：`merged/` 中 12 个分类 VPK，约 2.11 GiB，共 6631 个文件。
- 71 个源 MOD 均已纳入合并计划，没有遗漏或重复归类。
- 生成后的 12 个 VPK 均可重新解析。
- 已逐文件校验 6631 个 CRC，错误数为 0。
- 分类包之间只剩一个内容完全相同的重复文件：
  `particles/smoker_fx.pcf`。它不会造成实际覆盖差异。

## 分类包

| 输出 | 内容 | 大小 |
|---|---|---:|
| `01_UI_HUD.vpk` | 菜单、HUD、准星、手电筒、加载界面 | 17.57 MiB |
| `02_Survivors.vpk` | 生还者模型 | 94.47 MiB |
| `03_Infected.vpk` | 特感、普感、Tank、Witch 模型与资源 | 729.62 MiB |
| `04_Weapons.vpk` | 武器模型、皮肤和无后坐力参数 | 46.58 MiB |
| `05_Environment.vpk` | 天空、水面、植被、场景物件和涂鸦 | 344.23 MiB |
| `06_Effects.vpk` | 胆汁、枪口、Boomer、烟雾等粒子效果 | 26.56 MiB |
| `07_Audio.vpk` | 枪声音效和环境静音资源 | 113.66 MiB |
| `08_Gameplay.vpk` | Admin System、Bot 拿物、自动连跳 | 0.85 MiB |
| `09_Sprays.vpk` | 三组喷漆资源 | 16.10 MiB |
| `10_TUMTaRA.vpk` | TUMTaRA 主包及分包地图 | 401.68 MiB |
| `11_AlwaysToast_LDR.vpk` | Long Dead Road、地图资源和 Talker | 210.95 MiB |
| `12_Training_Map.vpk` | 终极特感训练地图 | 201.78 MiB |

## 已处理的冲突

1. UI
   - `2906960647.vpk` 优先于 `1195955268.vpk` 和 `2256147202.vpk` 的重名菜单文件。
   - `940974914.vpk` 优先提供 Addons 菜单文件。
   - `935497493.vpk` 与 `2906960647.vpk` 的 20 个重名文件内容一致，已自动去重。

2. 植被
   - `3559801225.vpk` 对 10 个重名树木资源拥有最终优先级。
   - `2228973955.vpk` 中不冲突的资源仍保留。

3. VScript 入口
   - `Admin System` 与 `Take bot item` 都修改
     `scripts/vscripts/director_base_addon.nut`。
   - 已生成组合入口，同时加载 `admin_system` 和 `take_bot_item`。

4. 喷漆清单
   - 两个 `scripts/sprays_manifest.txt` 已进行语义合并，不是简单覆盖。
   - 数字命名和 `test` 命名的两组喷漆条目均保留。

5. M60
   - M60 武器模型与无后坐力 MOD 修改同一武器参数文件。
   - 当前选择无后坐力版本作为最终参数，模型路径仍兼容。
   - 音效大包中排除了两个冲突的 M60 文件，让武器 MOD 自带音效生效。

## 被清理的内容

- 删除各源包重复的 `addoninfo.txt` 和 `addonimage.*`，每个分类包生成一个新的 `addoninfo.txt`。
- 排除 238 个位于 `source files/` 下的模型编译源文件。这些文件不会被游戏加载，只会增加 VPK 体积。
- 对同路径、同内容资源进行去重。

## 使用方式

1. 先备份或移走当前游戏 `left4dead2/addons/workshop` 中对应的原始 VPK。
2. 将 `merged/` 中需要的分类包复制到 `left4dead2/addons/`。
3. 不要同时启用原始包和合并包，否则仍会发生加载顺序覆盖。
4. 首次测试建议先启用 UI、角色、武器、环境等资源包，再分别测试地图和脚本包。

## 自动化工具可行性

可以制作，且本项目已经具备核心原型：

- `cmd/vpkaudit`：读取 VPK v1 目录、元数据、CRC 和重名冲突。
- `cmd/vpkmerge`：按 `merge-plan.json` 合并、去重、设置优先级、排除文件和注入修复文件。

可以完全自动处理的部分：

- VPK 完整性检查；
- 根据资源路径推断 UI、模型、音效、地图、脚本等类型；
- 同路径同 CRC 去重；
- 无冲突资源合并；
- 明确优先级后的覆盖；
- 输出后重新解析和 CRC 校验。

需要规则库或人工确认的部分：

- `.nut`、manifest、resource 等文本文件的语义合并；
- 两个 MOD 替换同一个角色、武器或模型时选择哪一个；
- 一个 MOD 同时包含地图、脚本、模型和声音时如何归类；
- MOD 间未写明的运行时依赖；
- 游戏内视觉、动画、声音缓存和脚本行为测试。

因此，自动工具适合采用“自动分析 + 冲突报告 + 用户选择 + 规则化合并”的流程。
纯粹无提示地把所有文件覆盖到一起并不可靠。

## 当前工具命令

```powershell
go run ./cmd/vpkaudit ./workshop ./audit-report.json
go run ./cmd/vpkmerge ./merge-plan.json
go run ./cmd/vpkaudit ./merged ./merged-audit-report.json
```

VPK v1 的单包数据区上限约为 4 GiB。当前最大输出包约 730 MiB，距离上限很远。
