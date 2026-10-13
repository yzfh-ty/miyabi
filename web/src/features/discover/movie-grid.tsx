import type { DiscoverMovie } from '@/api/discover'
import { MovieGridLayout } from '@/components/movie'
import { DiscoverMovieCard } from './movie-card'

export function MovieGrid({ movies }: { movies: DiscoverMovie[] }) {
  return (
    <MovieGridLayout>
      {movies.map(movie => (
        <DiscoverMovieCard key={movie.id} movie={movie} />
      ))}
    </MovieGridLayout>
  )
}
