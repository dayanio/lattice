<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import {
  useVueTable, getCoreRowModel, FlexRender, type ColumnDef,
} from '@tanstack/vue-table'
import {
  Server, Search, RefreshCw, MoreHorizontal, Plus, Trash2, Pencil,
  Wifi, WifiOff, Globe, ExternalLink, X, ChevronDown,
  ChevronLeft, ChevronRight, Copy, Check, Tag,
} from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter,
} from '@/components/ui/dialog'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem,
  DropdownMenuSeparator, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Card, CardContent } from '@/components/ui/card'
import AppAlertDialog from '@/components/AlertDialog.vue'
import { useMcpServerStore } from '@/stores/useMcpServerStore'
import type { MCPServer, MCPTool } from '@/api/mcp-server'

definePage({
  meta: { titleKey: 'manage.mcpServers.title', descKey: 'manage.mcpServers.desc' },
})

const { t } = useI18n()
const store = useMcpServerStore()

// ── Style maps ────────────────────────────────────────────────────
const phaseDot: Record<string, string> = {
  Ready: 'bg-emerald-500',
  Pending: 'bg-amber-400',
  Degraded: 'bg-rose-500',
}
const phaseBadge: Record<string, string> = {
  Ready: 'lattice-badge lattice-badge-stable',
  Pending: 'bg-amber-400/10 text-amber-600 dark:text-amber-400 ring-1 ring-amber-400/20',
  Degraded: 'bg-rose-500/10 text-rose-600 dark:text-rose-400 ring-1 ring-rose-500/20',
}
const modeBadge: Record<string, string> = {
  internal: 'bg-blue-500/10 text-blue-600 dark:text-blue-400 ring-1 ring-blue-500/20',
  external: 'bg-violet-500/10 text-violet-600 dark:text-violet-400 ring-1 ring-violet-500/20',
}
const riskColor: Record<string, string> = {
  low: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  medium: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
  high: 'bg-orange-500/10 text-orange-600 dark:text-orange-400',
  critical: 'bg-rose-500/10 text-rose-600 dark:text-rose-400',
}

// ── Stats / filter ────────────────────────────────────────────────
type PhaseFilter = 'all' | 'Ready' | 'Pending' | 'Degraded'
const phaseFilter = ref<PhaseFilter>('all')
const searchValue = ref('')

const stats = computed(() => {
  const all = store.rows
  return {
    total: all.length,
    ready: all.filter(s => s.status?.phase === 'Ready').length,
    pending: all.filter(s => s.status?.phase === 'Pending' || !s.status?.phase).length,
    degraded: all.filter(s => s.status?.phase === 'Degraded').length,
    internal: all.filter(s => s.status?.mode === 'internal').length,
  }
})

function setPhaseFilter(val: PhaseFilter) {
  phaseFilter.value = val
  searchValue.value = ''
}

const filtered = computed(() => {
  const q = searchValue.value.toLowerCase().trim()
  return store.rows.filter(s => {
    const matchSearch = !q
      || s.name.toLowerCase().includes(q)
      || s.spec.endpoint.toLowerCase().includes(q)
      || (s.spec.peerName ?? '').toLowerCase().includes(q)
    const matchPhase = phaseFilter.value === 'all' || (s.status?.phase ?? 'Pending') === phaseFilter.value
    return matchSearch && matchPhase
  })
})

// ── Pagination ─────────────────────────────────────────────────────
const PAGE_SIZE = 10
const currentPage = ref(1)
const totalPages = computed(() => Math.max(1, Math.ceil(filtered.value.length / PAGE_SIZE)))
const paged = computed(() => {
  const start = (currentPage.value - 1) * PAGE_SIZE
  return filtered.value.slice(start, start + PAGE_SIZE)
})
const visiblePages = computed(() => {
  const cur = currentPage.value
  const total = totalPages.value
  const start = Math.max(1, Math.min(cur - 1, total - 2))
  const end = Math.min(total, start + 2)
  return Array.from({ length: end - start + 1 }, (_, i) => start + i)
})
function goToPage(p: number) {
  if (p < 1 || p > totalPages.value) return
  currentPage.value = p
}

// ── Copy helper ───────────────────────────────────────────────────
const copiedKey = ref<string | null>(null)
async function copyText(text: string, key: string) {
  await navigator.clipboard.writeText(text)
  copiedKey.value = key
  setTimeout(() => { copiedKey.value = null }, 1500)
}

// ── Column definitions ─────────────────────────────────────────────
const columns = computed<ColumnDef<MCPServer>[]>(() => [
  {
    id: 'phase',
    header: t('manage.mcpServers.colPhase'),
    cell: ({ row }) => {
      const phase = row.original.status?.phase ?? 'Pending'
      return h('div', { class: 'flex items-center gap-2' }, [
        h('span', { class: 'relative flex size-2 shrink-0' }, [
          phase === 'Ready' && h('span', { class: `absolute inline-flex h-full w-full animate-ping rounded-full opacity-60 ${phaseDot[phase]}` }),
          h('span', { class: `relative inline-flex size-2 rounded-full ${phaseDot[phase] ?? 'bg-muted-foreground'}` }),
        ]),
        h('span', { class: `text-xs font-medium px-2 py-0.5 rounded-full ${phaseBadge[phase] ?? phaseBadge.Pending}` }, phase),
      ])
    },
  },
  {
    id: 'server',
    header: t('manage.mcpServers.colName'),
    cell: ({ row }) => {
      const s = row.original
      return h('div', { class: 'flex flex-col gap-0.5' }, [
        h('span', { class: 'font-semibold text-sm leading-none' }, s.name),
        s.spec.peerName && h('span', { class: 'font-mono text-[11px] text-muted-foreground/60' }, s.spec.peerName),
      ])
    },
  },
  {
    id: 'endpoint',
    header: t('manage.mcpServers.colURL'),
    cell: ({ row }) => {
      const endpoint = row.original.spec.endpoint
      const isUrl = endpoint.startsWith('http')
      return isUrl
        ? h('a', {
            href: endpoint, target: '_blank', rel: 'noopener noreferrer',
            class: 'font-mono text-xs text-muted-foreground hover:text-foreground transition-colors truncate max-w-[260px] inline-flex items-center gap-1',
          }, [endpoint, h(ExternalLink, { class: 'size-3 shrink-0' })])
        : h('span', { class: 'font-mono text-xs text-muted-foreground truncate max-w-[260px]' }, endpoint)
    },
  },
  {
    id: 'mode',
    header: t('manage.mcpServers.colMode'),
    cell: ({ row }) => {
      const mode = row.original.status?.mode
      if (!mode) return h('span', { class: 'text-[11px] text-muted-foreground/40' }, '—')
      return h('span', { class: `text-xs font-medium px-2 py-0.5 rounded-full ${modeBadge[mode] ?? ''}` }, mode)
    },
  },
  {
    id: 'tools',
    header: t('manage.mcpServers.colTools'),
    cell: ({ row }) => {
      const tools = row.original.spec.tools
      if (!tools?.length) return h('span', { class: 'text-[11px] text-muted-foreground/40' }, '—')
      const shown = tools.slice(0, 3)
      const rest = tools.length - shown.length
      return h('div', { class: 'flex flex-wrap gap-1' }, [
        ...shown.map(tool =>
          h('span', {
            class: `text-[11px] font-medium px-1.5 py-0.5 rounded ${riskColor[tool.riskLevel ?? 'low'] ?? riskColor.low}`,
            title: tool.description || tool.name,
          }, tool.name)
        ),
        rest > 0 && h('span', { class: 'text-[11px] text-muted-foreground px-1 py-0.5' }, `+${rest}`),
      ])
    },
  },
  {
    id: 'actions',
    header: '',
    cell: ({ row }) => {
      const srv = row.original
      return h(DropdownMenu, {}, {
        default: () => [
          h(DropdownMenuTrigger, { asChild: true }, () =>
            h(Button, { variant: 'ghost', size: 'sm', class: 'size-8 p-0' }, () =>
              h(MoreHorizontal, { class: 'size-4' })
            )
          ),
          h(DropdownMenuContent, { align: 'end', class: 'w-40' }, () => [
            h(DropdownMenuItem, { onClick: () => openDetail('view', srv) }, () => [
              h(ChevronRight, { class: 'mr-2 size-3.5' }), t('manage.mcpServers.actions.view'),
            ]),
            h(DropdownMenuItem, { onClick: () => openDetail('edit', srv) }, () => [
              h(Pencil, { class: 'mr-2 size-3.5' }), t('manage.mcpServers.actions.edit'),
            ]),
            h(DropdownMenuSeparator),
            h(DropdownMenuItem, {
              class: 'text-destructive focus:text-destructive',
              onClick: () => promptDelete(srv),
            }, () => [h(Trash2, { class: 'mr-2 size-3.5' }), t('common.action.delete')]),
          ]),
        ],
      })
    },
  },
])

// ── TanStack Table ─────────────────────────────────────────────────
const table = useVueTable({
  get data() { return paged.value },
  get columns() { return columns.value },
  getCoreRowModel: getCoreRowModel(),
  manualPagination: true,
  manualFiltering: true,
})

// ── Detail / Edit dialog ──────────────────────────────────────────
const detailOpen = ref(false)
const detailType = ref<'view' | 'edit'>('view')
const detailServer = ref<MCPServer | null>(null)
const editEndpoint = ref('')
const editPeerName = ref('')
const editTools = ref<MCPTool[]>([])
const detailToolsExpanded = ref(false)

function openDetail(type: 'view' | 'edit', srv: MCPServer) {
  detailType.value = type
  detailServer.value = srv
  editEndpoint.value = srv.spec.endpoint
  editPeerName.value = srv.spec.peerName ?? ''
  editTools.value = JSON.parse(JSON.stringify(srv.spec.tools ?? []))
  detailToolsExpanded.value = false
  detailOpen.value = true
}

function addEditTool() {
  editTools.value.push({ name: '', description: '', riskLevel: 'low' })
}
function removeEditTool(i: number) {
  editTools.value.splice(i, 1)
}

async function handleDetailSave() {
  if (!detailServer.value || !editEndpoint.value.trim()) return
  store.formName = detailServer.value.name
  store.formSpec.endpoint = editEndpoint.value.trim()
  const pn = editPeerName.value.trim()
  store.formSpec.peerName = pn || ''
  store.formSpec.tools = editTools.value.filter(t => t.name.trim())
  store.drawerType = 'edit'
  store.selectedServer = detailServer.value
  try {
    const ok = await store.handleCreateOrUpdate()
    if (ok) {
      toast.success(t('manage.mcpServers.editSuccess'))
      detailOpen.value = false
    } else {
      toast.error(t('manage.mcpServers.saveFailed'))
    }
  } catch (e: any) {
    toast.error(e?.message || t('manage.mcpServers.saveFailed'))
  }
}

// ── Create dialog ─────────────────────────────────────────────────
const toolSearch = ref('')
const toolsExpanded = ref(false)
const editingToolIdx = ref(-1)

function openCreate() {
  toolSearch.value = ''
  toolsExpanded.value = false
  editingToolIdx.value = -1
  store.openDrawer('create')
}

function toggleToolEdit(i: number) {
  editingToolIdx.value = editingToolIdx.value === i ? -1 : i
}

function addFormTool() {
  store.addTool()
}
function removeFormTool(i: number) {
  store.removeTool(i)
  if (editingToolIdx.value === i) editingToolIdx.value = -1
  else if (editingToolIdx.value > i) editingToolIdx.value--
}

async function handleCreate() {
  if (!store.formName.trim() || !store.formSpec.endpoint.trim()) return
  try {
    const ok = await store.handleCreateOrUpdate()
    if (ok) {
      toast.success(t('manage.mcpServers.createSuccess'))
    } else {
      toast.error(t('manage.mcpServers.saveFailed'))
    }
  } catch (e: any) {
    toast.error(e?.message || t('manage.mcpServers.saveFailed'))
  }
}

// ── Delete confirm ────────────────────────────────────────────────
const deleteTarget = ref<MCPServer | null>(null)
const deleteDialogOpen = ref(false)

function promptDelete(srv: MCPServer) {
  deleteTarget.value = srv
  deleteDialogOpen.value = true
}
async function confirmDelete() {
  if (!deleteTarget.value) return
  store.selectedServer = deleteTarget.value
  const ok = await store.handleDelete()
  if (ok) {
    toast.success(t('manage.mcpServers.deleteSuccess'))
    deleteDialogOpen.value = false
    deleteTarget.value = null
  } else {
    toast.error(t('manage.mcpServers.deleteError'))
  }
}

// ── Lifecycle ─────────────────────────────────────────────────────
onMounted(() => store.refresh())
</script>

<template>
  <div class="flex flex-col gap-5 p-6 animate-in fade-in duration-300">

    <!-- ── Stat cards ─────────────────────────────────────────────── -->
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
      <button
        class="border-border bg-card text-card-foreground rounded-xl border p-5 shadow-sm text-left hover:shadow-md transition-all"
        :class="phaseFilter === 'all' ? 'ring-2 ring-blue-500/20 border-blue-500/30' : ''"
        @click="setPhaseFilter('all')"
      >
        <div class="flex items-start justify-between">
          <div class="flex flex-col gap-1">
            <span class="text-muted-foreground text-sm font-medium">{{ t('manage.mcpServers.statTotal') }}</span>
            <span class="text-2xl font-bold tracking-tight">{{ stats.total }}</span>
          </div>
          <div class="bg-blue-500/10 rounded-lg p-2">
            <Server class="text-blue-500 size-4" />
          </div>
        </div>
        <div class="mt-3 flex items-center gap-1 text-xs text-muted-foreground">
          <Globe class="size-3.5 shrink-0 text-blue-500" />
          <span>{{ stats.internal }} {{ t('manage.mcpServers.statInternal') }}</span>
        </div>
      </button>

      <button
        class="border-border bg-card text-card-foreground rounded-xl border p-5 shadow-sm text-left hover:shadow-md transition-all"
        :class="phaseFilter === 'Ready' ? 'ring-2 ring-emerald-500/20 border-emerald-500/30' : ''"
        @click="setPhaseFilter('Ready')"
      >
        <div class="flex items-start justify-between">
          <div class="flex flex-col gap-1">
            <span class="text-muted-foreground text-sm font-medium">{{ t('manage.mcpServers.statReady') }}</span>
            <span class="text-2xl font-bold tracking-tight text-emerald-600 dark:text-emerald-400">{{ stats.ready }}</span>
          </div>
          <div class="bg-emerald-500/10 rounded-lg p-2">
            <Wifi class="text-emerald-500 size-4" />
          </div>
        </div>
        <div class="mt-3 flex items-center gap-1 text-xs text-muted-foreground">
          <Wifi class="size-3.5 shrink-0 text-emerald-500" />
          <span>{{ stats.total ? Math.round(stats.ready / stats.total * 100) : 0 }}% {{ t('manage.mcpServers.statReadyRate') }}</span>
        </div>
      </button>

      <button
        class="border-border bg-card text-card-foreground rounded-xl border p-5 shadow-sm text-left hover:shadow-md transition-all"
        :class="phaseFilter === 'Pending' ? 'ring-2 ring-amber-500/20 border-amber-500/30' : ''"
        @click="setPhaseFilter('Pending')"
      >
        <div class="flex items-start justify-between">
          <div class="flex flex-col gap-1">
            <span class="text-muted-foreground text-sm font-medium">{{ t('manage.mcpServers.statPending') }}</span>
            <span class="text-2xl font-bold tracking-tight text-amber-600 dark:text-amber-400">{{ stats.pending }}</span>
          </div>
          <div class="bg-amber-500/10 rounded-lg p-2">
            <WifiOff class="text-amber-500 size-4" />
          </div>
        </div>
        <div class="mt-3 flex items-center gap-1 text-xs text-muted-foreground">
          <WifiOff class="size-3.5 shrink-0 text-amber-500" />
          <span>{{ stats.pending === 0 ? t('manage.mcpServers.statAllReady') : t('manage.mcpServers.statWaiting') }}</span>
        </div>
      </button>

      <button
        class="border-border bg-card text-card-foreground rounded-xl border p-5 shadow-sm text-left hover:shadow-md transition-all"
        :class="phaseFilter === 'Degraded' ? 'ring-2 ring-rose-500/20 border-rose-500/30' : ''"
        @click="setPhaseFilter('Degraded')"
      >
        <div class="flex items-start justify-between">
          <div class="flex flex-col gap-1">
            <span class="text-muted-foreground text-sm font-medium">{{ t('manage.mcpServers.statDegraded') }}</span>
            <span class="text-2xl font-bold tracking-tight text-rose-600 dark:text-rose-400">{{ stats.degraded }}</span>
          </div>
          <div class="bg-rose-500/10 rounded-lg p-2">
            <WifiOff class="text-rose-500 size-4" />
          </div>
        </div>
        <div class="mt-3 flex items-center gap-1 text-xs text-muted-foreground">
          <WifiOff class="size-3.5 shrink-0 text-rose-500" />
          <span>{{ stats.degraded === 0 ? t('manage.mcpServers.statHealthy') : t('manage.mcpServers.statNeedsCheck') }}</span>
        </div>
      </button>
    </div>

    <!-- ── Toolbar ────────────────────────────────────────────────── -->
    <div class="flex items-center gap-2">
      <div class="relative w-72">
        <Search class="absolute left-2.5 top-1/2 -translate-y-1/2 size-4 text-muted-foreground" />
        <Input v-model="searchValue" :placeholder="t('manage.mcpServers.searchPlaceholder')" class="pl-8 h-9" />
      </div>
      <div class="ml-auto flex items-center gap-2">
        <Button variant="outline" size="sm" class="gap-1.5" :disabled="store.loading" @click="store.refresh()">
          <RefreshCw class="size-3.5" :class="store.loading ? 'animate-spin' : ''" />
          {{ t('common.action.refresh') }}
        </Button>
        <Button size="sm" class="gap-1.5" @click="openCreate">
          <Plus class="size-3.5" />
          {{ t('manage.mcpServers.register') }}
        </Button>
      </div>
    </div>

    <!-- ── Data Table ─────────────────────────────────────────────── -->
    <div class="rounded-md border">
      <Table>
        <TableHeader>
          <TableRow v-for="hg in table.getHeaderGroups()" :key="hg.id">
            <TableHead v-for="header in hg.headers" :key="header.id">
              <FlexRender v-if="!header.isPlaceholder" :render="header.column.columnDef.header" :props="header.getContext()" />
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <template v-if="table.getRowModel().rows.length">
            <TableRow v-for="row in table.getRowModel().rows" :key="row.id" class="cursor-pointer" @click="openDetail('view', row.original)">
              <TableCell v-for="cell in row.getVisibleCells()" :key="cell.id"
                @click.stop="cell.column.id === 'actions' ? undefined : openDetail('view', row.original)">
                <FlexRender :render="cell.column.columnDef.cell" :props="cell.getContext()" />
              </TableCell>
            </TableRow>
          </template>
          <TableRow v-else>
            <TableCell :colspan="columns.length" class="h-48 text-center">
              <template v-if="store.loading">
                <span class="text-sm text-muted-foreground">{{ t('common.status.loading') }}</span>
              </template>
              <template v-else>
                <div class="flex flex-col items-center justify-center gap-3 py-6">
                  <Server class="size-8 text-muted-foreground/40" />
                  <p class="text-sm text-muted-foreground">{{ t('manage.mcpServers.empty') }}</p>
                  <Button size="sm" class="gap-1.5" @click="openCreate">
                    <Plus class="size-3.5" />
                    {{ t('manage.mcpServers.registerFirst') }}
                  </Button>
                </div>
              </template>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>

    <!-- ── Pagination ─────────────────────────────────────────────── -->
    <div v-if="filtered.length > 0" class="flex items-center justify-between text-sm text-muted-foreground">
      <span>{{ t('common.pagination.total', { total: filtered.length, page: currentPage, totalPages }) }}</span>
      <div class="flex items-center gap-1">
        <Button variant="outline" size="sm" class="size-8 p-0" :disabled="currentPage <= 1" @click="goToPage(currentPage - 1)">
          <ChevronLeft class="size-4" />
        </Button>
        <Button v-for="p in visiblePages" :key="p" variant="outline" size="sm" class="size-8 p-0 text-xs"
          :class="p === currentPage ? 'bg-primary text-primary-foreground border-primary hover:bg-primary/90 hover:text-primary-foreground' : ''"
          @click="goToPage(p)">{{ p }}</Button>
        <Button variant="outline" size="sm" class="size-8 p-0" :disabled="currentPage >= totalPages" @click="goToPage(currentPage + 1)">
          <ChevronRight class="size-4" />
        </Button>
      </div>
    </div>

    <!-- ── Delete confirm ─────────────────────────────────────────── -->
    <AppAlertDialog
      v-model:open="deleteDialogOpen"
      :title="t('manage.mcpServers.deleteTitle')"
      :description="t('manage.mcpServers.confirmDelete', { name: deleteTarget?.name })"
      :confirm-text="t('common.action.delete')"
      variant="destructive"
      @confirm="confirmDelete"
      @cancel="deleteTarget = null"
    />
  </div>

  <!-- ── Create Dialog ──────────────────────────────────────────── -->
  <Dialog :open="store.isDrawerOpen && store.drawerType === 'create'" @update:open="v => { if (!v) store.isDrawerOpen = false }">
    <DialogContent class="sm:max-w-lg p-0 gap-0 overflow-hidden">
      <DialogHeader class="px-6 pt-6 pb-4 border-b">
        <DialogTitle class="flex items-center gap-2">
          <Server class="size-4" />
          {{ t('manage.mcpServers.registerTitle') }}
        </DialogTitle>
      </DialogHeader>

      <div class="px-6 py-5 space-y-4 max-h-[60vh] overflow-y-auto">
        <div class="space-y-1.5">
          <Label>{{ t('manage.mcpServers.formName') }}</Label>
          <Input v-model="store.formName" placeholder="my-mcp-server" class="h-9" />
        </div>
        <div class="space-y-1.5">
          <Label>{{ t('manage.mcpServers.formPeerName') }}</Label>
          <Input v-model="store.formSpec.peerName" :placeholder="t('manage.mcpServers.formPeerNamePlaceholder')" class="h-9" />
        </div>
        <div class="space-y-1.5">
          <Label>{{ t('manage.mcpServers.formURL') }}</Label>
          <div class="relative">
            <Input v-model="store.formSpec.endpoint" placeholder="https://mcp.example.com" class="h-9 pr-9"
              @input="store.autoDiscover()" />
            <RefreshCw v-if="store.discovering"
              class="absolute right-2.5 top-1/2 -translate-y-1/2 size-4 text-muted-foreground animate-spin" />
          </div>
          <p v-if="store.discovering" class="text-[11px] text-muted-foreground">{{ t('manage.mcpServers.discovering') }}</p>
          <p v-else-if="store.formSpec.tools?.length" class="text-[11px] text-emerald-600 dark:text-emerald-400">
            {{ t('manage.mcpServers.discoverSuccess', { count: store.formSpec.tools.length }) }}
          </p>
        </div>

        <Separator />

        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <Label>{{ t('manage.mcpServers.formTools') }}
              <span v-if="store.formSpec.tools?.length" class="text-xs font-normal text-muted-foreground ml-1">
                ({{ store.formSpec.tools.length }})
              </span>
            </Label>
            <Button variant="outline" size="sm" class="h-6 px-2 text-[11px] gap-1" @click="addFormTool">
              <Plus class="size-3" /> {{ t('manage.mcpServers.addTool') }}
            </Button>
          </div>

          <!-- Search (when many tools) -->
          <div v-if="(store.formSpec.tools?.length ?? 0) > 8" class="relative">
            <Search class="absolute left-2.5 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground" />
            <Input v-model="toolSearch" :placeholder="t('manage.mcpServers.searchTools')" class="pl-8 h-8 text-xs" />
          </div>

          <!-- Compact tag grid -->
          <div v-if="store.formSpec.tools?.length" class="rounded-lg border bg-muted/30 p-2.5 space-y-2"
            :class="toolsExpanded ? 'max-h-[240px] overflow-y-auto' : ''">
            <div class="flex flex-wrap gap-1.5">
              <template v-for="(tool, i) in store.formSpec.tools" :key="i">
                <button
                  v-show="!toolSearch || tool.name.toLowerCase().includes(toolSearch.toLowerCase())"
                  class="group inline-flex items-center gap-1 text-[11px] font-medium pl-2 pr-1 py-0.5 rounded-md cursor-pointer transition-colors"
                  :class="riskColor[tool.riskLevel ?? 'low'] ?? riskColor.low"
                  :title="tool.description || tool.name"
                  @click="toggleToolEdit(i)"
                >
                  <span class="truncate max-w-[140px]">{{ tool.name || '(unnamed)' }}</span>
                  <X class="size-3 opacity-0 group-hover:opacity-100 hover:text-destructive shrink-0"
                    @click.stop="removeFormTool(i)" />
                </button>
              </template>
            </div>

            <!-- Expand/collapse -->
            <button v-if="(store.formSpec.tools?.length ?? 0) > 12"
              class="w-full text-[11px] text-muted-foreground hover:text-foreground transition-colors py-1"
              @click="toolsExpanded = !toolsExpanded">
              {{ toolsExpanded ? t('manage.mcpServers.collapseTools') : t('manage.mcpServers.expandTools', { count: store.formSpec.tools!.length }) }}
            </button>
          </div>

          <!-- Inline editor for selected tool -->
          <div v-if="editingToolIdx >= 0 && store.formSpec.tools?.[editingToolIdx]" class="rounded-lg border p-3 space-y-2 bg-card">
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium text-muted-foreground">{{ t('manage.mcpServers.editTool') }}</span>
              <Button variant="ghost" size="icon" class="size-5" @click="editingToolIdx = -1">
                <X class="size-3" />
              </Button>
            </div>
            <Input v-model="store.formSpec.tools[editingToolIdx].name" placeholder="tool_name" class="h-8 text-xs" />
            <Input v-model="store.formSpec.tools[editingToolIdx].description" :placeholder="t('manage.mcpServers.formToolDesc')" class="h-8 text-xs" />
            <div class="flex items-center gap-2">
              <span class="text-[11px] text-muted-foreground shrink-0">Risk:</span>
              <DropdownMenu>
                <DropdownMenuTrigger as-child>
                  <Button variant="outline" size="sm" class="w-[90px] justify-between text-xs h-7">
                    {{ store.formSpec.tools[editingToolIdx].riskLevel ?? 'low' }}
                    <ChevronDown class="size-3 opacity-50" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent>
                  <DropdownMenuItem v-for="level in ['low', 'medium', 'high', 'critical']" :key="level"
                    @click="store.formSpec.tools![editingToolIdx].riskLevel = level as any">{{ level }}</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>

          <p v-if="!store.formSpec.tools?.length && !store.discovering" class="text-xs text-muted-foreground/50 py-1">
            {{ t('manage.mcpServers.noToolsHint') }}
          </p>
        </div>
      </div>

      <DialogFooter class="px-6 py-4 border-t bg-muted/30 sm:justify-end gap-2">
        <Button variant="outline" @click="store.isDrawerOpen = false">
          {{ t('common.action.cancel') }}
        </Button>
        <Button :disabled="store.loading || !store.formName.trim() || !store.formSpec.endpoint.trim()" @click="handleCreate">
          <RefreshCw v-if="store.loading" class="size-3.5 animate-spin mr-1.5" />
          {{ t('common.action.create') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>

  <!-- ── Detail / Edit Dialog ──────────────────────────────────── -->
  <Dialog :open="detailOpen" @update:open="v => { if (!v) detailOpen = false }">
    <DialogContent class="sm:max-w-lg p-0 gap-0 overflow-hidden">

      <!-- Header -->
      <DialogHeader class="px-6 pt-6 pb-5 border-b gap-0">
        <div class="flex items-start gap-3 pr-6">
          <div class="relative shrink-0 mt-0.5">
            <div
              class="size-10 rounded-lg border flex items-center justify-center"
              :class="detailServer?.status?.phase === 'Ready'
                ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-500'
                : detailServer?.status?.phase === 'Degraded'
                  ? 'bg-rose-500/10 border-rose-500/20 text-rose-500'
                  : 'bg-muted border-border text-muted-foreground'"
            >
              <Server class="size-4" />
            </div>
            <span
              class="absolute -bottom-1 -right-1 size-3 rounded-full border-2 border-background"
              :class="phaseDot[detailServer?.status?.phase ?? 'Pending']"
            >
              <span v-if="detailServer?.status?.phase === 'Ready'" class="absolute inset-0 rounded-full animate-ping opacity-75" :class="phaseDot['Ready']" />
            </span>
          </div>

          <div class="flex-1 min-w-0">
            <DialogTitle class="text-sm font-semibold leading-snug truncate">
              {{ detailServer?.name }}
            </DialogTitle>
            <p v-if="detailServer?.spec.peerName" class="text-xs text-muted-foreground font-mono mt-0.5 truncate">
              {{ detailServer.spec.peerName }}
            </p>
            <div class="flex items-center gap-2 mt-2 flex-wrap">
              <span v-if="detailServer?.status?.phase" class="inline-flex items-center text-[11px] font-medium px-2 py-0.5 rounded-md"
                :class="phaseBadge[detailServer.status.phase] ?? phaseBadge.Pending">
                {{ detailServer.status.phase }}
              </span>
              <span v-if="detailServer?.status?.mode" class="inline-flex items-center text-[11px] font-medium px-2 py-0.5 rounded-md"
                :class="modeBadge[detailServer.status.mode] ?? ''">
                {{ detailServer.status.mode }}
              </span>
            </div>
          </div>
        </div>
      </DialogHeader>

      <!-- Body -->
      <div v-if="detailServer" class="px-6 py-5 space-y-4 max-h-[55vh] overflow-y-auto">

        <!-- Endpoint card -->
        <Card class="rounded-lg shadow-none py-0">
          <CardContent class="px-4 py-3 flex items-center justify-between gap-3">
            <div class="flex items-center gap-3 min-w-0">
              <div class="size-8 rounded-md bg-muted flex items-center justify-center shrink-0">
                <Globe class="size-3.5 text-muted-foreground" />
              </div>
              <div class="min-w-0">
                <p class="text-[10px] text-muted-foreground leading-none mb-1">{{ t('manage.mcpServers.colURL') }}</p>
                <p class="font-mono text-sm font-semibold leading-none truncate">
                  {{ detailType === 'edit' ? editEndpoint : detailServer.spec.endpoint }}
                </p>
              </div>
            </div>
            <Button variant="ghost" size="icon" class="size-7 shrink-0 text-muted-foreground"
              @click="copyText(detailServer.spec.endpoint, 'endpoint')">
              <Check v-if="copiedKey === 'endpoint'" class="size-3.5 text-emerald-500" />
              <Copy v-else class="size-3.5" />
            </Button>
          </CardContent>
        </Card>

        <!-- Peer address -->
        <Card v-if="detailServer.status?.peerAddress" class="rounded-lg shadow-none py-0">
          <CardContent class="px-4 py-3 flex items-center justify-between gap-3">
            <div class="flex items-center gap-3 min-w-0">
              <div class="size-8 rounded-md bg-muted flex items-center justify-center shrink-0">
                <Server class="size-3.5 text-muted-foreground" />
              </div>
              <div class="min-w-0">
                <p class="text-[10px] text-muted-foreground leading-none mb-1">{{ t('manage.mcpServers.detail.peerAddress') }}</p>
                <p class="font-mono text-sm font-semibold leading-none truncate">{{ detailServer.status.peerAddress }}</p>
              </div>
            </div>
            <Button variant="ghost" size="icon" class="size-7 shrink-0 text-muted-foreground"
              @click="copyText(detailServer.status!.peerAddress!, 'peerAddr')">
              <Check v-if="copiedKey === 'peerAddr'" class="size-3.5 text-emerald-500" />
              <Copy v-else class="size-3.5" />
            </Button>
          </CardContent>
        </Card>

        <!-- Edit fields -->
        <template v-if="detailType === 'edit'">
          <Separator />
          <div class="space-y-1.5">
            <Label>{{ t('manage.mcpServers.formURL') }}</Label>
            <Input v-model="editEndpoint" placeholder="https://mcp.example.com" class="h-9" />
          </div>
          <div class="space-y-1.5">
            <Label>{{ t('manage.mcpServers.formPeerName') }}</Label>
            <Input v-model="editPeerName" :placeholder="t('manage.mcpServers.formPeerNamePlaceholder')" class="h-9" />
          </div>
        </template>

        <Separator />

        <!-- Tools list -->
        <div class="space-y-2">
          <p class="text-[11px] font-medium text-muted-foreground flex items-center gap-1.5">
            <Tag class="size-3" /> {{ t('manage.mcpServers.formTools') }}
          </p>

          <!-- View mode -->
          <div v-if="detailType === 'view'">
            <div v-if="detailServer.spec.tools?.length" class="rounded-lg border bg-muted/30 p-2.5 space-y-2"
              :class="detailToolsExpanded ? 'max-h-[240px] overflow-y-auto' : ''">
              <div class="flex flex-wrap gap-1.5">
                <span v-for="(tool, i) in detailServer.spec.tools" :key="i"
                  class="inline-flex items-center gap-1 text-[11px] font-medium px-2 py-0.5 rounded-md cursor-default"
                  :class="riskColor[tool.riskLevel ?? 'low'] ?? riskColor.low"
                  :title="tool.description || tool.name">
                  <span class="truncate max-w-[140px]">{{ tool.name }}</span>
                  <span v-if="tool.riskLevel && tool.riskLevel !== 'low'" class="opacity-60">{{ tool.riskLevel }}</span>
                </span>
              </div>
              <button v-if="detailServer.spec.tools.length > 12"
                class="w-full text-[11px] text-muted-foreground hover:text-foreground transition-colors py-1"
                @click="detailToolsExpanded = !detailToolsExpanded">
                {{ detailToolsExpanded ? t('manage.mcpServers.collapseTools') : t('manage.mcpServers.expandTools', { count: detailServer.spec.tools.length }) }}
              </button>
            </div>
            <p v-else class="text-xs text-muted-foreground/50 py-1">{{ t('manage.mcpServers.noToolsHint') }}</p>
          </div>

          <!-- Edit mode -->
          <div v-else class="space-y-2">
            <div v-for="(tool, i) in editTools" :key="i" class="flex items-center gap-2">
              <Input v-model="tool.name" placeholder="tool_name" class="flex-1 h-8 text-xs" />
              <Input v-model="tool.description" :placeholder="t('manage.mcpServers.formToolDesc')" class="flex-1 h-8 text-xs" />
              <DropdownMenu>
                <DropdownMenuTrigger as-child>
                  <Button variant="outline" size="sm" class="w-[80px] justify-between text-xs h-8">
                    {{ tool.riskLevel ?? 'low' }}
                    <ChevronDown class="size-3 opacity-50" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent>
                  <DropdownMenuItem v-for="level in ['low', 'medium', 'high', 'critical']" :key="level"
                    @click="tool.riskLevel = level as any">{{ level }}</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <Button variant="ghost" size="icon" class="size-7 text-destructive shrink-0" @click="removeEditTool(i)">
                <X class="size-3.5" />
              </Button>
            </div>
            <Button variant="outline" size="sm" class="gap-1 text-xs" @click="addEditTool">
              <Plus class="size-3" />{{ t('manage.mcpServers.addTool') }}
            </Button>
          </div>
        </div>
      </div>

      <!-- Footer -->
      <DialogFooter class="px-6 py-4 border-t bg-muted/30 sm:justify-between">
        <Button variant="ghost" size="sm" @click="detailOpen = false">
          {{ detailType === 'view' ? t('common.action.close') : t('common.action.cancel') }}
        </Button>
        <Button v-if="detailType === 'edit'" size="sm"
          :disabled="store.loading || !editEndpoint.trim()" @click="handleDetailSave">
          <RefreshCw v-if="store.loading" class="size-3.5 animate-spin mr-1.5" />
          {{ t('common.action.save') }}
        </Button>
        <Button v-else size="sm" variant="outline" @click="detailType = 'edit'">
          <Pencil class="size-3.5 mr-1.5" /> {{ t('manage.mcpServers.actions.edit') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
