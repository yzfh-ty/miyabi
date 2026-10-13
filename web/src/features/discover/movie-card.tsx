import type { DiscoverMovie } from '@/api/discover'
import { MovieCard } from '@/components/movie'
import { MovieResourceBadges, MovieStateBadge } from '@/components/movie/movie-badges'
import { MovieSubscribeButton } from './movie-subscribe-button'
import { MovieDetailTrigger } from '@/features/movie-detail/detail-trigger'

export function DiscoverMovieCard({ movie }: { movie: DiscoverMovie }) {
  const subscribable = movie.release_status === 'upcoming' && movie.magnets_count === 0
  return (
    <MovieDetailTrigger movie={{ id: movie.id }} className="block rounded-2xl outline-ring">
      <MovieCard
        movie={movie}
        description={movie.release_date}
        state={<MovieStateBadge movie={movie} />}
        coverOverlay={
          subscribable ? (
            <div className="absolute top-2 right-2">
              <MovieSubscribeButton movie={movie} />
            </div>
          ) : undefined
        }
      >
        <MovieResourceBadges movie={movie} />
      </MovieCard>
    </MovieDetailTrigger>
  )
}
