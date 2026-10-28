/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, type ReactNode } from 'react'
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

// Fork-only (ikun): invite-rebate settings. Settlement runs in an external
// job that reads IkunRebateEnabled / IkunRebateRate from the options table.

const schema = z.object({
  enabled: z.boolean(),
  ratePercent: z.coerce
    .number()
    .min(0.01, { message: 'Rate must be at least 0.01 and less than 100' })
    .lt(100, { message: 'Rate must be at least 0.01 and less than 100' })
    .multipleOf(0.01, { message: 'Rate must have at most 2 decimal places' }),
})

type Values = z.infer<typeof schema>

interface RebateInfo {
  enabled: boolean
  activity_start?: string
  timezone?: string
}

type ExclusionRole = 'inviter' | 'invitee'

interface Exclusion {
  id: number
  user_id: number
  role: ExclusionRole
  note: string
}

const exclusionsQueryKey = ['rebate-exclusions']

export function RebateSettingsSection({
  defaultValues,
}: {
  defaultValues: { enabled: boolean; rate: number }
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const [info, setInfo] = useState<RebateInfo>({ enabled: false })

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: {
      enabled: defaultValues.enabled,
      ratePercent: Number((defaultValues.rate * 100).toFixed(4)),
    },
  })
  const { isDirty, isSubmitting } = form.formState

  const loadInfo = () =>
    api
      .get('/api/rebate_info', { skipErrorHandler: true })
      .then((res) => setInfo((res.data as RebateInfo) ?? { enabled: false }))
      .catch(() => setInfo({ enabled: false }))

  useEffect(() => {
    void loadInfo()
  }, [])

  async function onSubmit(values: Values) {
    // Always write both keys: an unsaved default rate would leave the settler
    // without a rate while this page shows the switch as on.
    const rateResult = await updateOption.mutateAsync({
      key: 'IkunRebateRate',
      value: String(Number((values.ratePercent / 100).toFixed(6))),
    })
    if (!rateResult.success) {
      return
    }
    const enabledResult = await updateOption.mutateAsync({
      key: 'IkunRebateEnabled',
      value: String(values.enabled),
    })
    if (!enabledResult.success) {
      return
    }
    form.reset(values)
    void loadInfo()
  }

  return (
    <SettingsSection title={t('Invite rebate')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save invite rebate settings'
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable invite rebate')}</FormLabel>
                  <FormDescription>
                    {t(
                      'While off, daily settlement still runs but pays nothing, and the skipped days are never paid later.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={updateOption.isPending || isSubmitting}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <FormField
            control={form.control}
            name='ratePercent'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Rebate rate (%)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    step='0.01'
                    min={0.01}
                    max={99.99}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Applies from the next settlement. Must be at least 0.01 and less than 100.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='text-muted-foreground text-sm'>
            {info.activity_start
              ? t('Activity started on {{date}} ({{tz}})', {
                  date: info.activity_start,
                  tz: info.timezone,
                })
              : t('Settlement job not initialized yet')}
          </div>
        </SettingsForm>
      </Form>
      <RebateExclusionList />
    </SettingsSection>
  )
}

function RebateExclusionList() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const exclusions = useQuery({
    queryKey: exclusionsQueryKey,
    queryFn: async () => {
      const res = await api.get<{ success: boolean; data?: Exclusion[] }>(
        '/api/rebate/exclusions'
      )
      return requireServerSuccess(res.data).data ?? []
    },
    // The list shows its own error state with a retry button.
    meta: { errorToast: false },
  })
  const [userId, setUserId] = useState('')
  const [role, setRole] = useState<ExclusionRole>('inviter')
  const [note, setNote] = useState('')
  const [adding, setAdding] = useState(false)
  const [removingId, setRemovingId] = useState<number | null>(null)

  const roleItems = [
    { value: 'inviter', label: t('As inviter') },
    { value: 'invitee', label: t('As invitee') },
  ]

  const add = async () => {
    const id = Number(userId)
    if (!Number.isInteger(id) || id <= 0) {
      toast.error(t('Enter a valid user ID'))
      return
    }
    if (adding) return
    setAdding(true)
    try {
      const res = await api.post('/api/rebate/exclusions', {
        user_id: id,
        role,
        note,
      })
      if (!res.data?.success) {
        handleServerError(res.data, t('Operation failed'))
        return
      }
      setUserId('')
      setNote('')
      void queryClient.invalidateQueries({ queryKey: exclusionsQueryKey })
    } catch (e: unknown) {
      handleServerError(e, t('Operation failed'))
    } finally {
      setAdding(false)
    }
  }

  const remove = async (id: number) => {
    if (removingId !== null) return
    setRemovingId(id)
    try {
      const res = await api.delete(`/api/rebate/exclusions/${id}`)
      if (!res.data?.success) {
        handleServerError(res.data, t('Delete failed'))
        return
      }
      void queryClient.invalidateQueries({ queryKey: exclusionsQueryKey })
    } catch (e: unknown) {
      handleServerError(e, t('Delete failed'))
    } finally {
      setRemovingId(null)
    }
  }

  let list: ReactNode
  if (exclusions.isPending) {
    list = <LoadingState size='sm' className='min-h-16' />
  } else if (exclusions.isError) {
    list = (
      <div role='alert'>
        <ErrorState
          className='min-h-32'
          title={t('Failed to load excluded users')}
          description={getServerErrorMessage(exclusions.error)}
          onRetry={() => void exclusions.refetch()}
        />
      </div>
    )
  } else if (exclusions.data.length === 0) {
    list = (
      <p className='text-muted-foreground text-sm'>{t('No excluded users')}</p>
    )
  } else {
    list = (
      <table className='w-full text-sm'>
        <thead className='text-muted-foreground text-left'>
          <tr>
            <th className='py-1'>{t('User ID')}</th>
            <th className='py-1'>{t('Role')}</th>
            <th className='py-1'>{t('Note')}</th>
            <th className='py-1' />
          </tr>
        </thead>
        <tbody>
          {exclusions.data.map((row) => (
            <tr key={row.id} className='border-t'>
              <td className='py-1 tabular-nums'>{row.user_id}</td>
              <td className='py-1'>
                {row.role === 'inviter' ? t('As inviter') : t('As invitee')}
              </td>
              <td className='py-1'>{row.note}</td>
              <td className='py-1 text-right'>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => remove(row.id)}
                  disabled={removingId !== null}
                >
                  {t('Remove')}
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    )
  }

  return (
    <div className='mt-6 space-y-3'>
      <h4 className='text-sm font-semibold'>{t('Excluded users')}</h4>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Excluded users are never paid; removing them later does not back-pay.'
        )}
      </p>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          className='w-32'
          type='number'
          min={1}
          placeholder={t('User ID')}
          aria-label={t('User ID')}
          value={userId}
          onChange={(e) => setUserId(e.target.value)}
        />
        <Select
          items={roleItems}
          value={role}
          onValueChange={(value) => {
            if (value === 'inviter' || value === 'invitee') setRole(value)
          }}
        >
          <SelectTrigger className='h-9 w-36' aria-label={t('Role')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {roleItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <Input
          className='w-48'
          placeholder={t('Note')}
          aria-label={t('Note')}
          maxLength={255}
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
        <Button type='button' size='sm' onClick={add} disabled={adding}>
          {t('Add exclusion')}
        </Button>
      </div>
      {list}
    </div>
  )
}
