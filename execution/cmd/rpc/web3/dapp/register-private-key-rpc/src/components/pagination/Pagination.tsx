import { memo } from 'react'
import { DOTS, usePagination } from '~/hooks/usePagination'
import { ChevronLeft, ChevronRight } from 'lucide-react'

type PaginationProps = {
  siblingCount?: number
  totalPageCount: number
  onPageChange: (page: number) => void
  currentPage: number
}

const Pagination: React.FC<PaginationProps> = ({ siblingCount = 1, totalPageCount, currentPage, onPageChange }) => {
  const paginationRange = usePagination({ siblingCount, currentPage, totalPageCount })
  return (
    <div className='flex items-center justify-center flex-wrap gap-2 p-4'>
      <button
        type='button'
        disabled={currentPage === 1}
        className={`w-10 h-10 flex items-center justify-center rounded-lg border transition-all
          ${
            currentPage === 1
              ? 'cursor-not-allowed bg-app-secondary dark:bg-app-secondary text-foreground-muted dark:text-foreground-muted border-border opacity-50'
              : 'bg-card dark:bg-card text-foreground dark:text-foreground border-border dark:border-border hover:bg-primary/10 dark:hover:bg-primary/20 hover:text-primary dark:hover:text-primary hover:border-primary dark:hover:border-primary font-semibold'
          }`}
        onClick={() => onPageChange(currentPage - 1)}
      >
        <ChevronLeft className='w-5 h-5' />
      </button>

      {paginationRange?.map((el: string | number, index: number) => {
        if (el === DOTS) {
          return (
            <span key={index} className='w-10 h-10 flex items-center justify-center text-gray-400 dark:text-gray-500'>
              ...
            </span>
          )
        }

        const isActive = currentPage === el
        return (
          <button
            key={index}
            className={`w-10 h-10 flex items-center justify-center rounded-lg border transition-all font-medium
              ${
                isActive
                  ? 'bg-primary dark:bg-primary text-white dark:text-white border-primary dark:border-primary shadow-md font-semibold'
                  : 'bg-card dark:bg-card text-foreground dark:text-foreground border-border dark:border-border hover:bg-primary/10 dark:hover:bg-primary/20 hover:text-primary dark:hover:text-primary hover:border-primary dark:hover:border-primary font-semibold'
              }`}
            onClick={() => onPageChange(el as number)}
          >
            {el}
          </button>
        )
      })}

      <button
        type='button'
        disabled={currentPage === totalPageCount}
        className={`w-10 h-10 flex items-center justify-center rounded-lg border transition-all
          ${
            currentPage === totalPageCount
              ? 'cursor-not-allowed bg-app-secondary dark:bg-app-secondary text-foreground-muted dark:text-foreground-muted border-border opacity-50'
              : 'bg-card dark:bg-card text-foreground dark:text-foreground border-border dark:border-border hover:bg-primary/10 dark:hover:bg-primary/20 hover:text-primary dark:hover:text-primary hover:border-primary dark:hover:border-primary font-semibold'
          }`}
        onClick={() => onPageChange(currentPage + 1)}
      >
        <ChevronRight className='w-5 h-5' />
      </button>
    </div>
  )
}

export default memo(Pagination)
