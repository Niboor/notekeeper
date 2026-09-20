import { Link } from 'react-router'
import { t } from '../i18n'

/** Shown for an address that leads nowhere, inside the signed-in frame. */
export function NotFound() {
  return (
    <div className="board-hint">
      <div>
        <h1>{t('notfound.title')}</h1>
        <p>{t('notfound.lead')}</p>
        <p>
          <Link className="btn" to="/">
            {t('notfound.home')}
          </Link>
        </p>
      </div>
    </div>
  )
}
