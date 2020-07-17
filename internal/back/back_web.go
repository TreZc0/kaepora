package back

// This file contains functions specific to the web frontend.
// Please do not call them outside of the webserver.

import (
	"kaepora/internal/util"
	"time"

	"github.com/jmoiron/sqlx"
)

func (b *Back) GetLeaderboardForShortcode(shortcode string, maxDeviation int) (out []LeaderboardEntry, _ error) {
	if err := b.transaction(func(tx *sqlx.Tx) (err error) {
		out, err = b.getLeaderboardForShortcode(tx, shortcode, maxDeviation)
		return err
	}); err != nil {
		return nil, err
	}

	return out, nil
}

func (b *Back) getLeaderboardForShortcode(
	tx *sqlx.Tx,
	shortcode string,
	maxDeviation int,
) ([]LeaderboardEntry, error) {
	league, err := getLeagueByShortCode(tx, shortcode)
	if err != nil {
		return nil, err
	}

	bans := b.config.DiscordBannedUserIDs
	if len(bans) == 0 {
		bans = []string{"0"}
	}

	query, args, err := sqlx.In(`
            SELECT
                Player.Name AS PlayerName,
                Player.StreamURL AS PlayerStreamURL,
                PlayerRating.Rating AS Rating,
                PlayerRating.Deviation AS Deviation,
                SUM(CASE WHEN MatchEntry.Outcome = ? THEN 1 ELSE 0 END) AS Wins,
                SUM(CASE WHEN MatchEntry.Outcome = ? THEN 1 ELSE 0 END) AS Losses,
                SUM(CASE WHEN MatchEntry.Outcome = ? THEN 1 ELSE 0 END) AS Draws,
                SUM(CASE WHEN MatchEntry.Status = ? THEN 1 ELSE 0 END) AS Forfeits
            FROM PlayerRating
            INNER JOIN Player ON(PlayerRating.PlayerID = Player.ID)
            LEFT JOIN MatchEntry ON(PlayerRating.PlayerID = MatchEntry.PlayerID AND MatchEntry.Status != ?)
            LEFT JOIN Match ON(Match.ID = MatchEntry.MatchID)
            WHERE
                Match.LeagueID = ?
                AND PlayerRating.LeagueID = ?
                AND PlayerRating.Deviation < ?
                AND (Player.DiscordID NOT IN(?) OR Player.DiscordID IS NULL)
            GROUP BY Player.ID
            ORDER BY PlayerRating.Rating DESC
        `,
		MatchEntryOutcomeWin,
		MatchEntryOutcomeLoss,
		MatchEntryOutcomeDraw,
		MatchEntryStatusForfeit,
		MatchEntryStatusInProgress,
		league.ID, league.ID,
		maxDeviation,
		bans,
	)
	if err != nil {
		return nil, err
	}

	query = tx.Rebind(query)
	var ret []LeaderboardEntry
	if err := tx.Select(&ret, query, args...); err != nil {
		return nil, err
	}

	return ret, nil
}

func (b *Back) GetLeagues() (ret []League, _ error) {
	return ret, b.transaction(func(tx *sqlx.Tx) (err error) {
		ret, err = getLeagues(tx)
		return err
	})
}

func (b *Back) GetLeagueByShortcode(shortcode string) (ret League, _ error) {
	return ret, b.transaction(func(tx *sqlx.Tx) (err error) {
		ret, err = getLeagueByShortCode(tx, shortcode)
		return err
	})
}

func (b *Back) GetLeague(id util.UUIDAsBlob) (ret League, _ error) {
	return ret, b.transaction(func(tx *sqlx.Tx) (err error) {
		ret, err = getLeagueByID(tx, id)
		return err
	})
}

// GetMatchSessions returns sessions in a timeframe that have the given
// statuses ordered by the given SQL clause.
// The leagues the sessions belong to are returned indexed by their ID.
func (b *Back) GetMatchSessions(
	fromDate, toDate time.Time,
	statuses []MatchSessionStatus,
	order string,
) ([]MatchSession, map[util.UUIDAsBlob]League, error) {
	var (
		sessions []MatchSession
		leagues  map[util.UUIDAsBlob]League
	)

	if err := b.transaction(func(tx *sqlx.Tx) error {
		query, args, err := sqlx.In(`
            SELECT * FROM MatchSession
            WHERE DATETIME(StartDate) >= DATETIME(?) AND
                  DATETIME(StartDate) <= DATETIME(?) AND
                  Status IN(?)`,
			fromDate, toDate, statuses,
		)
		if err != nil {
			return err
		}
		query = tx.Rebind(query) + ` ORDER BY ` + order

		if err := tx.Select(&sessions, query, args...); err != nil {
			return err
		}
		ids := make([]util.UUIDAsBlob, 0, len(sessions))
		for k := range sessions {
			ids = append(ids, sessions[k].LeagueID)
		}

		if len(ids) == 0 {
			return nil
		}

		query, args, err = sqlx.In(`SELECT * FROM League WHERE ID IN (?)`, ids)
		if err != nil {
			return err
		}
		query = tx.Rebind(query)
		sLeagues := make([]League, 0, len(ids))
		leagues = make(map[util.UUIDAsBlob]League, len(ids))
		if err := tx.Select(&sLeagues, query, args...); err != nil {
			return err
		}
		for _, v := range sLeagues {
			leagues[v.ID] = v
		}

		return nil
	}); err != nil {
		return nil, nil, err
	}

	return sessions, leagues, nil
}

func (b *Back) GetPlayerRatings(shortcode string) (ret []PlayerRating, _ error) {
	return ret, b.transaction(func(tx *sqlx.Tx) (err error) {
		league, err := getLeagueByShortCode(tx, shortcode)
		if err != nil {
			return err
		}

		if err := tx.Select(
			&ret,
			`SELECT * FROM PlayerRating WHERE LeagueID = ? AND Deviation < ?`,
			league.ID, DeviationThreshold,
		); err != nil {
			return err
		}

		return nil
	})
}

// GetMatchSession returns a single session and all its matches and players indexed by their ID.
func (b *Back) GetMatchSession(id util.UUIDAsBlob) (
	session MatchSession,
	matches []Match,
	players map[util.UUIDAsBlob]Player,
	_ error,
) {
	if err := b.transaction(func(tx *sqlx.Tx) (err error) {
		session, err = getMatchSessionByID(tx, id)
		if err != nil {
			return err
		}

		matches, err = getMatchesBySessionID(tx, id)
		if err != nil {
			return err
		}

		players, err = getPlayersByMatches(tx, matches)
		if err != nil {
			return err
		}

		return nil
	}); err != nil {
		return MatchSession{}, nil, nil, err
	}

	return session, matches, players, nil
}

func (b *Back) GetMatch(id util.UUIDAsBlob) (match Match, _ error) {
	if err := b.transaction(func(tx *sqlx.Tx) (err error) {
		match, err = getMatchByID(tx, id)
		if err != nil {
			return err
		}

		return nil
	}); err != nil {
		return Match{}, err
	}

	return match, nil
}
